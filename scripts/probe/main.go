// Command probe sends a fixed set of requests to the GoDaddy Domains API and
// writes a Markdown transcript of what came back. It answers the questions
// in "Phase 0: live probes" of
// docs/superpowers/specs/2026-11-26-safe-dns-resources-design.md.
//
// It writes to a live DNS zone. Run it only against a domain that is not in
// use. See scripts/probe/README.md.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// probeNames are the values -probe accepts.
var probeNames = []string{"p1", "p2", "p3", "p4", "p5", "p6", "p7", "p8", "p9", "p10", "p12", "p13", "p14", "p15", "p16", "p17", "p18", "p19"}

// readOnly lists the probes that send no writes. They run without a zone
// snapshot.
var readOnly = map[string]bool{"p1": true, "p7": true, "p8": true, "p12": true}

// errInterrupted is what a run returns when the user interrupted it and
// nothing else went wrong.
var errInterrupted = errors.New("interrupted; the steps that put things back and check the zone were completed")

// The environment variables that hold p16's two under-scoped tokens.
const (
	readOnlyTokenEnv = "GODADDY_PROBE_READONLY_TOKEN"
	dnsOnlyTokenEnv  = "GODADDY_PROBE_DNSONLY_TOKEN"
)

const interruptNotice = "interrupted: finishing safely, do not close this terminal"

func main() {
	probe := flag.String("probe", "", "which probe to run: "+strings.Join(probeNames, " ")+" (p6 includes p11)")
	outDir := flag.String("out", "scripts/probe/out", "directory for transcripts")
	allowZoneWide := flag.Bool("allow-zone-wide", false, "permit p6 (nameservers), p9 and p13 (type-wide replace of AAAA), p16 (PATCH of the current nameservers), p17 (type-wide replace of TXT, MX and SRV); p18 and p19 need it too")
	replaceReal := flag.Bool("replace-real-records", false, "p18 and p19 only: permit removing real records for a short time (p18: all A and CNAME records; p19: the _domainconnect CNAME); the probe puts them back")
	targetNS := flag.String("target-ns", "", "p6 only: comma-separated nameservers to move the domain to")
	flag.Parse()

	if err := run(*probe, *outDir, *allowZoneWide, *replaceReal, *targetNS); err != nil {
		fmt.Fprintln(os.Stderr, "probe:", err)
		os.Exit(exitCode(err))
	}
}

// exitCode is 130 for a run that was interrupted and otherwise went well,
// and 1 for every other failure, including a failure in an interrupted run.
func exitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errInterrupted):
		return 130
	}
	return 1
}

func validateProbe(probe string) error {
	for _, known := range probeNames {
		if probe == known {
			return nil
		}
	}
	return fmt.Errorf("unknown probe %q; choose one of: %s", probe, strings.Join(probeNames, " "))
}

// newHTTPClient returns the client the probes use. It never follows a
// redirect: a redirected write would go to a path the guard has not seen,
// and the redirect itself is what a probe wants to record.
func newHTTPClient() *http.Client {
	return &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// run checks its arguments, opens the transcript and runs one probe.
// Nothing is created or sent before the arguments are known to be good.
func run(probe, outDir string, allowZoneWide, replaceReal bool, targetNS string) error {
	if err := validateProbe(probe); err != nil {
		return err
	}
	if probe == "p18" && !(allowZoneWide && replaceReal) {
		return fmt.Errorf("p18 removes the zone's real A and CNAME records and then restores them; " +
			"pass both -allow-zone-wide and -replace-real-records to confirm")
	}
	if probe == "p19" && !(allowZoneWide && replaceReal) {
		return fmt.Errorf("p19 changes and deletes the zone's real _domainconnect CNAME record and then restores it; " +
			"pass both -allow-zone-wide and -replace-real-records to confirm")
	}
	token := os.Getenv("GODADDY_API_TOKEN")
	domain := strings.ToLower(strings.TrimSpace(os.Getenv("GODADDY_PROBE_DOMAIN")))
	if token == "" || domain == "" {
		return fmt.Errorf("set GODADDY_API_TOKEN and GODADDY_PROBE_DOMAIN; there is no default domain")
	}
	base := os.Getenv("GODADDY_PROBE_BASEURL")
	if base == "" {
		base = "https://api.godaddy.com"
	}

	var targets []string
	if probe == "p6" {
		for _, h := range strings.Split(targetNS, ",") {
			if h = strings.TrimSpace(h); h != "" {
				targets = append(targets, h)
			}
		}
		if len(targets) < 2 {
			return fmt.Errorf("p6 needs -target-ns with at least two nameservers")
		}
	}
	if _, needsFlag := zoneWide[probe]; needsFlag && !allowZoneWide {
		return fmt.Errorf("%s changes more than probe records; pass -allow-zone-wide to confirm", probe)
	}

	args := probeArgs{targets: targets}
	if probe == "p16" {
		args.readOnlyToken = strings.TrimSpace(os.Getenv(readOnlyTokenEnv))
		args.dnsOnlyToken = strings.TrimSpace(os.Getenv(dnsOnlyTokenEnv))
		var missing []string
		if args.readOnlyToken == "" {
			missing = append(missing, readOnlyTokenEnv)
		}
		if args.dnsOnlyToken == "" {
			missing = append(missing, dnsOnlyTokenEnv)
		}
		if len(missing) > 0 {
			return fmt.Errorf("p16 needs two more tokens; not set: %s", strings.Join(missing, ", "))
		}
	}

	idBytes := make([]byte, 3)
	if _, err := rand.Read(idBytes); err != nil {
		return err
	}
	runID := hex.EncodeToString(idBytes)

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	outPath := filepath.Join(outDir, probe+"-"+runID+".md")
	file, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer file.Close()

	r := &runner{
		base:         base,
		token:        token,
		domain:       domain,
		runID:        runID,
		prefix:       "_probe-" + runID + "-",
		allow:        allowanceFor(probe, allowZoneWide),
		gap:          time.Second,
		pollEvery:    10 * time.Second,
		restoreEvery: 15 * time.Second,
		watchUnit:    time.Second,
		// The file comes first, so it still gets the transcript if the
		// terminal has gone away.
		out:  io.MultiWriter(file, os.Stdout),
		hc:   newHTTPClient(),
		stop: make(chan struct{}),
	}

	// A signal only records that the user wants to stop. This goroutine
	// sends no request and touches nothing of the runner but its interrupt
	// flag; the probe unwinds on its own goroutine. See runner.interrupt.
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	// Without this, a write to a closed stdout or stderr pipe would kill the
	// process before it could put anything back.
	signal.Ignore(syscall.SIGPIPE)
	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		for range signals {
			fmt.Fprintln(os.Stderr, interruptNotice)
			r.interrupt()
		}
	}()
	defer func() {
		signal.Stop(signals)
		close(signals)
		<-handlerDone
	}()

	return execute(r, probe, args, outPath)
}

// probeArgs is what a probe may need beyond the runner.
type probeArgs struct {
	targets       []string     // p6: nameservers to move the domain to
	readOnlyToken string       // p16: a token with the scope domains.domain:read only
	dnsOnlyToken  string       // p16: a token with domains.domain:read and domains.dns:update
	before        []zoneRecord // the zone before the probe; execute fills it in
}

// probes maps each name in probeNames to its function.
var probes = map[string]func(*runner, probeArgs) error{
	"p1":  func(r *runner, _ probeArgs) error { p1(r); return nil },
	"p2":  func(r *runner, _ probeArgs) error { p2(r); return nil },
	"p3":  func(r *runner, _ probeArgs) error { p3(r); return nil },
	"p4":  func(r *runner, _ probeArgs) error { p4(r); return nil },
	"p5":  func(r *runner, _ probeArgs) error { p5(r); return nil },
	"p6":  func(r *runner, a probeArgs) error { return p6(r, a.targets) },
	"p7":  func(r *runner, _ probeArgs) error { p7(r); return nil },
	"p8":  func(r *runner, _ probeArgs) error { p8(r); return nil },
	"p9":  func(r *runner, a probeArgs) error { return p9(r, a.before) },
	"p10": func(r *runner, _ probeArgs) error { p10(r); return nil },
	"p12": func(r *runner, _ probeArgs) error { p12(r); return nil },
	"p13": func(r *runner, a probeArgs) error { return p13(r, a.before) },
	"p14": func(r *runner, _ probeArgs) error { p14(r); return nil },
	"p15": func(r *runner, _ probeArgs) error { p15(r); return nil },
	"p16": p16,
	"p17": func(r *runner, a probeArgs) error { return p17(r, a.before) },
	"p18": func(r *runner, a probeArgs) error { return p18(r, a.before) },
	"p19": func(r *runner, a probeArgs) error { return p19(r, a.before) },
}

// execute runs one probe with a prepared runner: snapshot, probe, then the
// finishing steps in finish. Those are deferred, so they run however the
// probe ends: normally, after an interrupt, or with a panic. They run on
// this goroutine.
func execute(r *runner, probe string, args probeArgs, outPath string) (err error) {
	fn, known := probes[probe]
	if !known {
		return validateProbe(probe)
	}
	fmt.Fprintf(r.out, "## Probe %s on %s\n\nDate: %s. Run ID: %s. Record prefix: `%s`.\n",
		strings.ToUpper(probe), r.domain, time.Now().UTC().Format("2006-01-02 15:04 MST"), r.runID, r.prefix)

	var before []zoneRecord
	if !readOnly[probe] {
		if before, err = r.snapshot(); err != nil {
			if r.interrupted() {
				fmt.Fprint(r.out, "\n**Interrupted before any write was sent.**\n")
				return errInterrupted
			}
			return fmt.Errorf("before any write: %w", err)
		}
		fmt.Fprintf(r.out, "\nZone snapshot before: %d records (%s).\n", len(before), countByType(before))
	}

	var probeErr error
	defer func() {
		var failures []string
		if p := recover(); p != nil {
			failures = append(failures, fmt.Sprintf("probe panicked: %v", p))
		}
		// The probe's own failure goes last: for p6 it names the
		// nameservers, and that must be the last thing on the screen.
		err = r.finish(!readOnly[probe], before, failures, probeErr, outPath)
	}()
	args.before = before
	probeErr = fn(r, args)
	return nil
}

// finish puts things back and checks: it deletes the probe's records, reads
// the zone again and compares. An interrupt does not stop its requests. The
// last thing it writes to the transcript is what went wrong, if anything,
// and its result is the result of the run.
func (r *runner) finish(wrote bool, before []zoneRecord, failures []string, probeErr error, outPath string) error {
	r.finishing = true

	if wrote {
		r.cleanup()
		fmt.Fprint(r.out, "\n### Zone check\n")
		after, err := r.snapshot()
		if err != nil {
			failures = append(failures, fmt.Sprintf("the zone could not be read after the probe (%s); compare the zone by hand", err))
		} else {
			fmt.Fprintf(r.out, "\nZone snapshot after: %d records (%s).\n", len(after), countByType(after))
			if changes := diffOutside(before, after, r.prefix); len(changes) > 0 {
				fmt.Fprintf(r.out, "\n**RECORDS OUTSIDE THE PROBE PREFIX CHANGED:**\n\n```\n%s\n```\n", strings.Join(changes, "\n"))
				failures = append(failures, fmt.Sprintf("%d record(s) outside the probe prefix changed; see %s", len(changes), outPath))
			} else {
				fmt.Fprint(r.out, "\nNo record outside the probe prefix changed.\n")
			}
			var left []string
			for _, z := range after {
				if strings.HasPrefix(z.Name, r.prefix) {
					fmt.Fprintf(r.out, "\nLeft behind, delete by hand: %s\n", z)
					left = append(left, z.Type+" "+z.Name)
				}
			}
			if len(left) > 0 {
				failures = append(failures, fmt.Sprintf("%d probe record(s) left behind; delete by hand: %s",
					len(left), strings.Join(left, ", ")))
			}
		}
	}
	if probeErr != nil {
		failures = append(failures, probeErr.Error())
	}

	fmt.Fprintf(r.out, "\nTranscript: %s\n", outPath)
	wasInterrupted := r.interrupted()
	if wasInterrupted {
		fmt.Fprint(r.out, "\n**Interrupted. The steps that put things back and check the zone still ran.**\n")
	}
	for _, failure := range failures {
		fmt.Fprintf(r.out, "\n**FAILED: %s**\n", failure)
	}
	switch {
	case len(failures) > 0 && wasInterrupted:
		return fmt.Errorf("%s; the run was also interrupted", strings.Join(failures, "; "))
	case len(failures) > 0:
		return errors.New(strings.Join(failures, "; "))
	case wasInterrupted:
		return errInterrupted
	}
	return nil
}
