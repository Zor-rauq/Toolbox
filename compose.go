// Package compose lit le fichier compose sur la VM et en tire les informations
// dont la toolbox a besoin : nom du service, image attendue, chemins de donnees.
//
// Le fichier compose est la source de verite. La toolbox ne l'ecrit jamais :
// sinon elle deviendrait elle-meme une source de derive, et le benefice de
// l'avoir choisi comme reference serait perdu.
package compose

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"toolbox/internal/ssh"
)

// Service est la vue minimale dont la toolbox a besoin.
type Service struct {
	Image      string        `yaml:"image"`
	PullPolicy string        `yaml:"pull_policy"`
	Volumes    []VolumeEntry `yaml:"volumes"`
}

// File est un fichier compose charge depuis la VM.
type File struct {
	Path     string              `yaml:"-"`
	Dir      string              `yaml:"-"`
	Services map[string]*Service `yaml:"services"`
}

// VolumeEntry accepte les deux syntaxes de volume : la courte
// ("/hote:/conteneur:ro") et la longue (source/target).
type VolumeEntry struct {
	Source string
	Target string
}

// UnmarshalYAML gere les deux syntaxes.
func (v *VolumeEntry) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		parts := strings.Split(node.Value, ":")
		switch len(parts) {
		case 1:
			v.Target = parts[0]
		default:
			v.Source = parts[0]
			v.Target = parts[1]
		}
		return nil
	}
	var long struct {
		Source string `yaml:"source"`
		Target string `yaml:"target"`
	}
	if err := node.Decode(&long); err != nil {
		return err
	}
	v.Source, v.Target = long.Source, long.Target
	return nil
}

// candidates liste les noms usuels cherches lorsqu'un repertoire est declare.
var candidates = []string{
	"podman-compose.yml", "podman-compose.yaml",
	"docker-compose.yml", "docker-compose.yaml",
	"compose.yml", "compose.yaml",
}

// Resolve transforme un chemin declare (fichier ou repertoire) en chemin de
// fichier compose sur la VM. Une ambiguite fait echouer la resolution plutot
// que de choisir a la place de l'operateur.
func Resolve(c *ssh.Client, declared string) (string, error) {
	p := c.Expand(declared)

	fi, err := c.Stat(p)
	if err != nil {
		return "", fmt.Errorf("chemin compose introuvable sur la VM : %s", p)
	}
	if !fi.IsDir() {
		return p, nil
	}

	var found []string
	for _, name := range candidates {
		if c.Exists(path.Join(p, name)) {
			found = append(found, path.Join(p, name))
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", fmt.Errorf(
			"aucun fichier compose dans %s (noms cherches : %s) ; "+
				"declarez le chemin complet dans compose_files",
			p, strings.Join(candidates, ", "))
	default:
		return "", fmt.Errorf(
			"plusieurs fichiers compose dans %s (%s) ; "+
				"declarez le chemin complet dans compose_files",
			p, strings.Join(found, ", "))
	}
}

// Load lit et interprete le fichier compose distant.
func Load(c *ssh.Client, declared string) (*File, error) {
	p, err := Resolve(c, declared)
	if err != nil {
		return nil, err
	}
	raw, err := c.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var f File
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("compose distant illisible (%s) : %w", p, err)
	}
	if len(f.Services) == 0 {
		return nil, fmt.Errorf("aucun service declare dans %s", p)
	}
	f.Path = p
	f.Dir = path.Dir(p)
	return &f, nil
}

// FindService renvoie le service demande, ou tente de le deduire du nom du
// composant. En cas d'ambiguite, la deduction echoue et exige une declaration
// explicite : la toolbox ne devine pas.
func (f *File) FindService(declared, component string) (string, *Service, error) {
	if declared != "" {
		s, ok := f.Services[declared]
		if !ok {
			return "", nil, fmt.Errorf("service %q absent du compose %s (services : %s)",
				declared, f.Path, strings.Join(f.ServiceNames(), ", "))
		}
		return declared, s, nil
	}

	var matches []string
	for name, s := range f.Services {
		if name == component || strings.Contains(imageName(s.Image), component) {
			matches = append(matches, name)
		}
	}
	sort.Strings(matches)

	switch len(matches) {
	case 1:
		return matches[0], f.Services[matches[0]], nil
	case 0:
		return "", nil, fmt.Errorf(
			"aucun service du compose ne correspond au composant %q ; "+
				"renseignez 'service' dans deploy.yaml ou passez --service (services : %s)",
			component, strings.Join(f.ServiceNames(), ", "))
	default:
		return "", nil, fmt.Errorf(
			"plusieurs services correspondent au composant %q (%s) ; "+
				"renseignez 'service' dans deploy.yaml ou passez --service",
			component, strings.Join(matches, ", "))
	}
}

// ServiceNames renvoie les noms de services tries.
func (f *File) ServiceNames() []string {
	out := make([]string, 0, len(f.Services))
	for k := range f.Services {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// DataVolume renvoie le chemin hote du volume monte sur le repertoire de
// donnees. En cas de candidats multiples, l'operateur doit trancher.
func (s *Service) DataVolume(hint string) (string, error) {
	var matches []string
	for _, v := range s.Volumes {
		if v.Source == "" {
			continue
		}
		if hint != "" && !strings.Contains(v.Target, hint) {
			continue
		}
		matches = append(matches, v.Source)
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf(
			"aucun volume hote ne correspond au repertoire de donnees ; "+
				"renseignez 'data_path' dans deploy.yaml")
	default:
		return "", fmt.Errorf(
			"plusieurs volumes candidats (%s) ; renseignez 'data_path' dans deploy.yaml",
			strings.Join(matches, ", "))
	}
}

// CheckPullPolicy signale une politique qui ferait echouer le up en
// environnement coupe du reseau : podman tenterait un pull vers un registre
// injoignable.
func (s *Service) CheckPullPolicy() error {
	switch strings.ToLower(strings.TrimSpace(s.PullPolicy)) {
	case "always", "newer":
		return fmt.Errorf(
			"le service declare pull_policy=%q : le demarrage tentera un pull "+
				"vers un registre injoignable depuis cette VM. Passez la politique "+
				"a 'never' ou 'missing' dans le compose avant de deployer", s.PullPolicy)
	}
	return nil
}

// SplitImage separe une reference d'image en depot et etiquette.
func SplitImage(ref string) (repo, tag string) {
	if ref == "" {
		return "", ""
	}
	// Une etiquette ne peut pas contenir de '/', ce qui distingue le tag d'un
	// port de registre (registre:5000/depot).
	if i := strings.LastIndex(ref, ":"); i > 0 && !strings.Contains(ref[i+1:], "/") {
		return ref[:i], ref[i+1:]
	}
	return ref, "latest"
}

func imageName(ref string) string {
	repo, _ := SplitImage(ref)
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		return repo[i+1:]
	}
	return repo
}
