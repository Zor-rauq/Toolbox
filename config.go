// Package config porte les deux descripteurs de la toolbox et la cascade de
// resolution : flag CLI > variable d'environnement > fichier > echec explicite.
//
// Il n'y a volontairement aucun repli silencieux : une valeur absente et
// non deductible fait echouer la commande avec un message qui nomme la cle
// manquante et l'endroit ou la renseigner.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Type enumere les formes d'artefact prises en charge.
type Type string

const (
	TypeEarWar       Type = "ear-war"
	TypeQuarkusZip   Type = "quarkus-zip"
	TypeQuarkusImage Type = "quarkus-image"
)

// EnvDev est la seule valeur d'environnement que la toolbox accepte.
// Ce garde-fou empeche l'outil de deriver vers le chemin de livraison.
const EnvDev = "dev"

// Build decrit la delegation du build au projet.
type Build struct {
	// Script est le chemin du script de build, relatif au repertoire du composant.
	Script string `yaml:"script"`
	// Env ajoute des variables au processus de build.
	Env map[string]string `yaml:"env"`
}

// ArtifactSpec decrit ou trouver l'artefact produit.
type ArtifactSpec struct {
	// Path est un motif glob resolu apres le build. Il doit correspondre a
	// exactement un fichier, sans quoi la resolution echoue.
	Path string `yaml:"path"`
	// Manifest est le chemin du manifeste optionnel emis par le script de build.
	// S'il existe, il prend le pas sur Path.
	Manifest string `yaml:"manifest"`
}

// ImageSpec porte les reglages propres au type quarkus-image.
type ImageSpec struct {
	// SourceRef est la reference portee par l'image a l'interieur de l'archive
	// produite par podman save. Vide, elle est lue dans la sortie de podman load,
	// dont le format varie selon les versions. La declarer supprime cette
	// dependance.
	SourceRef string `yaml:"source_ref"`
}

// Component est le descripteur versionne dans le depot du projet (deploy.yaml).
type Component struct {
	Name     string       `yaml:"component"`
	Type     Type         `yaml:"type"`
	Build    Build        `yaml:"build"`
	Artifact ArtifactSpec `yaml:"artifact"`
	Image    ImageSpec    `yaml:"image"`

	// DataPath force le repertoire de donnees sur la VM pour le type
	// quarkus-zip, lorsque la deduction depuis les volumes du compose est ambigue.
	DataPath string `yaml:"data_path"`

	// Service est le nom du service dans le compose. Vide, il est deduit du
	// compose distant ; si la deduction est ambigue, la commande echoue.
	Service string `yaml:"service"`
	// Version sert a etiqueter les images et les sauvegardes. Vide, elle est
	// deduite du depot Git.
	Version string `yaml:"version"`

	// dir est le repertoire du descripteur, utilise pour resoudre les chemins relatifs.
	dir string `yaml:"-"`
}

// Dir renvoie le repertoire racine du composant.
func (c *Component) Dir() string { return c.dir }

// Target est une VM cible, declaree hors du depot.
type Target struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	User string `yaml:"user"`
	// Env doit valoir "dev". Toute autre valeur bloque l'execution.
	Env string `yaml:"env"`

	// ComposeFiles liste les fichiers compose sur la VM. Un chemin de
	// repertoire est accepte : les noms usuels y sont cherches, et une
	// ambiguite fait echouer la resolution.
	ComposeFiles []string `yaml:"compose_files"`
	ComposeBin   string   `yaml:"compose_bin"`
	PodmanBin    string   `yaml:"podman_bin"`

	// WildflyDeployments est le repertoire de deploiement WildFly sur la VM.
	WildflyDeployments string `yaml:"wildfly_deployments"`

	// KnownHosts surcharge le fichier known_hosts utilise pour la verification
	// de l'empreinte du serveur.
	KnownHosts string `yaml:"known_hosts"`

	name string `yaml:"-"`
}

// Name renvoie la cle de la cible dans le fichier de cibles.
func (t *Target) Name() string { return t.name }

// Addr renvoie l'adresse de connexion SSH.
func (t *Target) Addr() string { return fmt.Sprintf("%s:%d", t.Host, t.Port) }

type targetsFile struct {
	Targets map[string]*Target `yaml:"targets"`
}

// Overrides porte les surcharges issues de la ligne de commande.
type Overrides struct {
	Component string
	Type      string
	Service   string
	Version   string
	Artifact  string
	User      string
}

// LoadComponent lit le descripteur du composant puis applique la cascade.
func LoadComponent(path string, ov Overrides) (*Component, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("descripteur illisible (%s) : %w", path, err)
	}
	var c Component
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("descripteur invalide (%s) : %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	c.dir = filepath.Dir(abs)

	// Cascade : le fichier a deja ete lu, on superpose l'environnement puis les flags.
	applyEnv(&c.Name, "TOOLBOX_COMPONENT")
	applyEnvType(&c.Type, "TOOLBOX_TYPE")
	applyEnv(&c.Service, "TOOLBOX_SERVICE")
	applyEnv(&c.Version, "TOOLBOX_VERSION")
	applyEnv(&c.Artifact.Path, "TOOLBOX_ARTIFACT_PATH")

	applyFlag(&c.Name, ov.Component)
	if ov.Type != "" {
		c.Type = Type(ov.Type)
	}
	applyFlag(&c.Service, ov.Service)
	applyFlag(&c.Version, ov.Version)
	applyFlag(&c.Artifact.Path, ov.Artifact)

	if c.Build.Script == "" {
		c.Build.Script = "./build.sh"
	}
	if c.Artifact.Manifest == "" {
		c.Artifact.Manifest = ".toolbox/artifact.json"
	}

	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Component) validate() error {
	if c.Name == "" {
		return fmt.Errorf("champ 'component' manquant : renseignez-le dans deploy.yaml ou passez --component")
	}
	switch c.Type {
	case TypeEarWar, TypeQuarkusZip, TypeQuarkusImage:
	case "":
		return fmt.Errorf("champ 'type' manquant : valeurs acceptees %s, %s, %s",
			TypeEarWar, TypeQuarkusZip, TypeQuarkusImage)
	default:
		return fmt.Errorf("type d'artefact inconnu : %q", c.Type)
	}
	if c.Artifact.Path == "" {
		return fmt.Errorf("champ 'artifact.path' manquant : la toolbox ne devine pas l'emplacement de l'artefact")
	}
	return nil
}

// LoadTarget lit le fichier de cibles et renvoie la cible demandee.
func LoadTarget(path, name string, ov Overrides) (*Target, error) {
	if name == "" {
		return nil, fmt.Errorf("aucune cible indiquee : passez --target")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("fichier de cibles illisible (%s) : %w", path, err)
	}
	var tf targetsFile
	if err := yaml.Unmarshal(raw, &tf); err != nil {
		return nil, fmt.Errorf("fichier de cibles invalide (%s) : %w", path, err)
	}
	t, ok := tf.Targets[name]
	if !ok || t == nil {
		return nil, fmt.Errorf("cible %q absente de %s (cibles connues : %s)",
			name, path, strings.Join(keys(tf.Targets), ", "))
	}
	t.name = name

	applyEnv(&t.User, "TOOLBOX_USER")
	applyFlag(&t.User, ov.User)

	if t.Port == 0 {
		t.Port = 22
	}
	if t.ComposeBin == "" {
		t.ComposeBin = "podman-compose"
	}
	if t.PodmanBin == "" {
		t.PodmanBin = "podman"
	}
	if t.WildflyDeployments == "" {
		t.WildflyDeployments = "$HOME/current/data/wildfly_data/deployments"
	}
	if len(t.ComposeFiles) == 0 {
		t.ComposeFiles = []string{"$HOME/current"}
	}

	if err := t.validate(); err != nil {
		return nil, err
	}
	return t, nil
}

func (t *Target) validate() error {
	if t.Host == "" {
		return fmt.Errorf("cible %q : champ 'host' manquant", t.name)
	}
	if t.User == "" {
		return fmt.Errorf("cible %q : champ 'user' manquant", t.name)
	}
	if t.Env != EnvDev {
		// Garde-fou volontairement non contournable par flag ou variable.
		return fmt.Errorf(
			"cible %q : env vaut %q, or cette toolbox ne s'adresse qu'a %q. "+
				"Les autres environnements passent par la chaine de livraison.",
			t.name, t.Env, EnvDev)
	}
	return nil
}

// DefaultTargetsPath renvoie l'emplacement par defaut du fichier de cibles.
func DefaultTargetsPath() string {
	if v := os.Getenv("TOOLBOX_TARGETS"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "targets.yaml"
	}
	return filepath.Join(home, ".config", "toolbox", "targets.yaml")
}

func applyEnv(dst *string, key string) {
	if v := os.Getenv(key); v != "" {
		*dst = v
	}
}

func applyEnvType(dst *Type, key string) {
	if v := os.Getenv(key); v != "" {
		*dst = Type(v)
	}
}

func applyFlag(dst *string, v string) {
	if v != "" {
		*dst = v
	}
}

func keys(m map[string]*Target) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
