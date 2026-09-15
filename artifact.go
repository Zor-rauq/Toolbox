// Package artifact declenche le build local et localise l'artefact produit.
//
// Le build est entierement delegue au script du projet : la toolbox reste
// agnostique du systeme de construction, ce qui lui permet de servir tous les
// archetypes sans modification.
//
// La localisation de l'artefact suit la cascade : manifeste emis par le script
// s'il existe, sinon motif declare dans deploy.yaml, sinon echec explicite.
package artifact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"toolbox/internal/config"
	"toolbox/internal/ui"
)

// Artifact decrit le fichier a deposer sur la VM.
type Artifact struct {
	Path    string
	Name    string
	Version string
	SHA256  string
}

// Manifest est le contrat optionnel qu'un script de build peut emettre pour
// lever une ambiguite. Quand il est present, il prime sur le motif declare.
type Manifest struct {
	Path    string `json:"path"`
	Version string `json:"version"`
	Name    string `json:"name"`
}

// Build execute le script de build du projet.
func Build(c *config.Component, log *ui.Logger) error {
	script := c.Build.Script
	if !filepath.IsAbs(script) {
		script = filepath.Join(c.Dir(), script)
	}
	if _, err := os.Stat(script); err != nil {
		return fmt.Errorf("script de build introuvable (%s) : %w", script, err)
	}

	cmd := exec.Command("/bin/sh", script)
	cmd.Dir = c.Dir()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	for k, v := range c.Build.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	log.Info("build : %s (depuis %s)", script, c.Dir())
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("le script de build a echoue : %w", err)
	}
	return nil
}

// Resolve localise l'artefact et calcule son empreinte.
func Resolve(c *config.Component, log *ui.Logger) (*Artifact, error) {
	a := &Artifact{Name: c.Name, Version: c.Version}

	if m, err := readManifest(c); err != nil {
		return nil, err
	} else if m != nil {
		log.Debug("manifeste trouve, il prime sur artifact.path")
		a.Path = absFrom(c.Dir(), m.Path)
		if m.Version != "" {
			a.Version = m.Version
		}
		if m.Name != "" {
			a.Name = m.Name
		}
	} else {
		p, err := resolveGlob(c)
		if err != nil {
			return nil, err
		}
		a.Path = p
	}

	fi, err := os.Stat(a.Path)
	if err != nil {
		return nil, fmt.Errorf("artefact introuvable (%s) : %w", a.Path, err)
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("artefact attendu sous forme de fichier, %s est un repertoire", a.Path)
	}

	if a.Version == "" {
		a.Version = detectVersion(c.Dir())
	}
	if a.Version == "" {
		return nil, fmt.Errorf(
			"version indeterminable : renseignez 'version' dans deploy.yaml, " +
				"passez --version, ou emettez-la dans le manifeste")
	}

	sum, err := sha256File(a.Path)
	if err != nil {
		return nil, err
	}
	a.SHA256 = sum

	log.Info("artefact : %s (%s, %s)", filepath.Base(a.Path), a.Version, humanSize(fi.Size()))
	return a, nil
}

func readManifest(c *config.Component) (*Manifest, error) {
	p := absFrom(c.Dir(), c.Artifact.Manifest)
	raw, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("manifeste illisible (%s) : %w", p, err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("manifeste invalide (%s) : %w", p, err)
	}
	if m.Path == "" {
		return nil, fmt.Errorf("manifeste %s : champ 'path' manquant", p)
	}
	return &m, nil
}

func resolveGlob(c *config.Component) (string, error) {
	pattern := absFrom(c.Dir(), c.Artifact.Path)
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", fmt.Errorf("motif artifact.path invalide (%s) : %w", c.Artifact.Path, err)
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf(
			"aucun fichier ne correspond a artifact.path (%s) ; "+
				"le build a-t-il bien produit l'artefact ?", c.Artifact.Path)
	default:
		return "", fmt.Errorf(
			"artifact.path (%s) correspond a %d fichiers : %s. "+
				"Affinez le motif ou emettez un manifeste depuis le script de build",
			c.Artifact.Path, len(matches), strings.Join(shorten(matches), ", "))
	}
}

// detectVersion lit la version depuis Git, sans imposer de systeme de build.
func detectVersion(dir string) string {
	if out, err := run(dir, "git", "describe", "--tags", "--always", "--dirty"); err == nil {
		return out
	}
	if out, err := run(dir, "git", "rev-parse", "--short", "HEAD"); err == nil {
		return out
	}
	return ""
}

func run(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func absFrom(dir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(dir, p)
}

func shorten(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = filepath.Base(p)
	}
	return out
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d o", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %co", float64(n)/float64(div), "kMGT"[exp])
}
