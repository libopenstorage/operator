package manifest

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"
)

const (
	httpPrefix  = "http://"
	httpsPrefix = "https://"
)

// Methods to override for testing
var (
	httpGet = http.Get
	CAfiles []string
)

func getManifestFromURL(manifestURL string, proxy string) ([]byte, error) {
	var resp *http.Response
	var err error
	if proxy == "" {
		resp, err = httpGet(manifestURL)
		if err != nil {
			return nil, err
		}
	} else {
		// Add the http prefix so we can parse the URL:
		// Wrong format: url.Parse("127.0.0.1:3213")
		// Correct format: url.Parse("http://127.0.0.1:3213")
		// If no http or https prefix is given, we assume it is http.
		if !strings.HasPrefix(strings.ToLower(proxy), httpPrefix) &&
			!strings.HasPrefix(strings.ToLower(proxy), httpsPrefix) {
			proxy = httpPrefix + proxy
		}

		proxyURL, err := url.Parse(proxy)
		if err != nil {
			return nil, err
		}

		caCertPool := x509.NewCertPool()
		caCertPool.AppendCertsFromPEM(addCAfiles(CAfiles))

		client := &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
				TLSClientConfig: &tls.Config{
					RootCAs: caCertPool,
				},
			},
		}

		resp, err = client.Get(manifestURL)
		if err != nil {
			return nil, err
		}
	}

	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func parseVersionManifest(content []byte) (*Version, error) {
	manifest := &Version{}
	err := yaml.Unmarshal(content, manifest)
	if err != nil {
		return nil, err
	}
	if manifest.PortworxVersion == "" {
		return nil, ErrReleaseNotFound
	}
	return manifest, nil
}

func addCAfiles(files []string) error {
	var (
		cb        *certs.CertBundle
		certFiles []string
		basedir   string
		certList  []*x509.Certificate
	)

	if len(files) <= 0 {
		// if no custom certs given, still check the CA-config if old certs need removing
		goto lab_rewrite_conf
	}

	for _, s := range files {
		buff, err := ioutil.ReadFile(s)
		if err != nil {
			return fmt.Errorf("could not read CA-file %s: %s", s, err)
		} else if len(buff) <= 0 {
			return fmt.Errorf("empty file provided: %s", s)
		}
		cb = certs.AppendPEM(cb, buff)
	}
	if errs := cb.Check(); len(errs) > 0 {
		for _, err := range errs {
			logrus.Error(err.Error())
		}
		return fmt.Errorf("errors processing CA certificates")
	}

	certList = cb.GetCertificates(true)
	if len(certList) <= 0 {
		logrus.Warn("No actual CA certificates found in passed CA files")
		return nil
	}

	basedir = path.Join("rootfs/usr/share/ca-certificates/pwx")
	if st, err := os.Stat(basedir); err != nil && os.IsNotExist(err) {
		if err = os.MkdirAll(basedir, 0777); err != nil {
			return fmt.Errorf("could not create %s dir: %s", basedir, err)
		}
	} else if err == nil && !st.IsDir() {
		return fmt.Errorf("INTERNAL ERROR - %s exists, but not a directory", basedir)
	} else if err != nil {
		return fmt.Errorf("could not stat %s: %s", basedir, err)
	}

	// dump certificates
	for _, cert := range certList {
		hash := md5.Sum(cert.Raw)
		fingerprint := hex.EncodeToString(hash[:])
		fname := "pwx/" + fingerprint + ".crt"
		fullpath := path.Join(basedir, fingerprint) + ".crt"
		certFiles = append(certFiles, fname)
		if _, err := os.Stat(fullpath); err == nil {
			logrus.Infof("> CA cert already installed - subject: %s  fingerprint: %s", cert.Subject, fingerprint)
			continue
		}
		if err := ioutil.WriteFile(fullpath, certs.CertToPEM(cert), 0444); err != nil {
			return fmt.Errorf("error writing %s: %s", fullpath, err)
		}
		logrus.Infof("> Adding CA cert - subject: %s  fingerprint: %s", cert.Subject, fingerprint)
	}
	sort.Strings(certFiles)

	// rewrite config
lab_rewrite_conf:
	confName := path.Join("rootfs/etc/ca-certificates.conf")
	confContent, err := ioutil.ReadFile(confName)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("error reading %s: %s", confName, err)
	}
	outbuf := bytes.Buffer{}
	lines := bytes.Split(confContent, []byte("\n"))
	prefix := []byte("pwx/")
	for _, line := range lines {
		if bytes.HasPrefix(line, prefix) {
			logrus.Debugf("> skipping line %q", line)
			continue
		} else if len(line) <= 0 {
			logrus.Debug("> skipping empty line")
			continue
		}
		outbuf.Write(line)
		outbuf.WriteRune('\n')
	}
	for _, fname := range certFiles {
		outbuf.WriteString(fname)
		outbuf.WriteRune('\n')
	}
	newContent := outbuf.Bytes()
	if bytes.Equal(confContent, newContent) {
		logrus.Info("No change in CA certificates")
		return nil
	}
	logrus.Info("Refreshing CA certificates")
	newConfName := confName + ".tmp"
	if err = ioutil.WriteFile(newConfName, newContent, 0444); err != nil {
		return fmt.Errorf("error writing %s: %s", newConfName, err)
	} else if err = os.Rename(newConfName, confName); err != nil {
		return fmt.Errorf("error replacing %s: %s", confName, err)
	}

	// run update-ca-certificates under chroot

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	outbuf.Reset()
	cmd := exec.CommandContext(ctx, "/usr/sbin/update-ca-certificates")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Chroot:     path.Join("rootfs"),
		Cloneflags: syscall.CLONE_NEWNS,
	}
	// note, the `chroot` command will do a "chdir(/)", but this shell-script can't, so we always get
	// "sh: 0: getcwd() failed: No such file or directory" in the output -- therefore hiding the output unless errors
	cmd.Stdout, cmd.Stderr = &outbuf, &outbuf

	err = cmd.Run()
	if err != nil {
		logrus.WithError(err).Errorf("Error running update-ca-certificates in portworx.service container:")
		fmt.Fprintf(logrus.StandardLogger().Out, "OUTPUT> %s\n", outbuf.Bytes())
	}
	return err
}
