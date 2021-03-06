package trivy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/aquasecurity/harbor-scanner-trivy/pkg/etc"
	log "github.com/sirupsen/logrus"
	"golang.org/x/xerrors"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
)

// RegistryAuth wraps registry credentials.
type RegistryAuth struct {
	Username string
	Password string
}

type Wrapper interface {
	Run(imageRef string, auth RegistryAuth, insecureRegistry bool) (ScanReport, error)
}

type wrapper struct {
	config etc.Trivy
}

func NewWrapper(config etc.Trivy) Wrapper {
	return &wrapper{
		config: config,
	}
}

func (w *wrapper) Run(imageRef string, auth RegistryAuth, insecureRegistry bool) (report ScanReport, err error) {
	log.WithField("image_ref", imageRef).Debug("Started scanning")

	executable, err := exec.LookPath("trivy")
	if err != nil {
		return report, err
	}

	reportFile, err := ioutil.TempFile(w.config.ReportsDir, "scan_report_*.json")
	if err != nil {
		return report, err
	}
	log.WithField("path", reportFile.Name()).Debug("Saving scan report to tmp file")
	defer func() {
		log.WithField("path", reportFile.Name()).Debug("Removing scan report tmp file")
		err := os.Remove(reportFile.Name())
		if err != nil {
			log.WithError(err).Warn("Error while removing scan report file")
		}
	}()

	args := []string{
		"--no-progress",
		"--cache-dir", w.config.CacheDir,
		"--severity", w.config.Severity,
		"--vuln-type", w.config.VulnType,
		"--format", "json",
		"--output", reportFile.Name(),
		imageRef,
	}

	if w.config.IgnoreUnfixed {
		args = append([]string{"--ignore-unfixed"}, args...)
	}

	if w.config.DebugMode {
		args = append([]string{"--debug"}, args...)
	}

	log.WithFields(log.Fields{"cmd": executable, "args": args}).Trace("Exec command with args")

	cmd := exec.Command(executable, args...)

	cmd.Env = os.Environ()
	if auth.Username != "" && auth.Password != "" {
		cmd.Env = append(cmd.Env,
			fmt.Sprintf("TRIVY_USERNAME=%s", auth.Username),
			fmt.Sprintf("TRIVY_PASSWORD=%s", auth.Password))
	}
	if insecureRegistry {
		cmd.Env = append(cmd.Env, fmt.Sprintf("TRIVY_NON_SSL=true"))
	}

	stderrBuffer := bytes.Buffer{}

	cmd.Stderr = &stderrBuffer

	stdout, err := cmd.Output()
	if err != nil {
		log.WithFields(log.Fields{
			"image_ref": imageRef,
			"exit_code": cmd.ProcessState.ExitCode(),
			"std_err":   stderrBuffer.String(),
			"std_out":   string(stdout),
		}).Error("Running trivy failed")
		return report, xerrors.Errorf("running trivy: %v: %v", err, stderrBuffer.String())
	}

	log.WithFields(log.Fields{
		"image_ref": imageRef,
		"exit_code": cmd.ProcessState.ExitCode(),
		"std_err":   stderrBuffer.String(),
		"std_out":   string(stdout),
	}).Debug("Running trivy finished")

	report, err = w.parseScanReports(reportFile)
	return
}

func (w *wrapper) parseScanReports(reportFile io.Reader) (report ScanReport, err error) {
	var scanReports []ScanReport
	err = json.NewDecoder(reportFile).Decode(&scanReports)
	if err != nil {
		return report, xerrors.Errorf("decoding scan report from file %w", err)
	}

	if len(scanReports) == 0 {
		return report, xerrors.New("expected at least one report")
	}

	// Collect all vulnerabilities to single scanReport to allow showing those in Harbor
	report.Target = scanReports[0].Target
	report.Vulnerabilities = []Vulnerability{}
	for _, scanReport := range scanReports {
		log.WithField("target", scanReport.Target).Trace("Parsing vulnerabilities")
		report.Vulnerabilities = append(report.Vulnerabilities, scanReport.Vulnerabilities...)
	}

	return
}
