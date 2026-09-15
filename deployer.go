// Package deploy porte le socle commun aux types d'artefact et l'interface
// que chaque type implemente.
//
// Regle de separation : un Plan ne fait que lire l'etat de la VM, seules les
// Steps ecrivent. C'est ce qui rend --dry-run honnete sans code dedie.
//
// Ajouter un type d'artefact revient a ecrire un fichier dans ce package et a
// l'enregistrer dans le registre ci-dessous. Rien d'autre ne bouge.
package deploy

import (
	"fmt"

	"toolbox/internal/artifact"
	"toolbox/internal/compose"
	"toolbox/internal/config"
	"toolbox/internal/ssh"
	"toolbox/internal/ui"
)

// Step est une action unitaire du plan.
type Step struct {
	Desc string
	Run  func() error
}

// Context rassemble tout ce dont un Deployer a besoin.
type Context struct {
	Comp    *config.Component
	Target  *config.Target
	SSH     *ssh.Client
	Compose *compose.File
	Log     *ui.Logger
	DryRun  bool

	// Artifact est nil pour rollback et status.
	Artifact *artifact.Artifact

	// Renseignes par ResolveService.
	Service     string
	ServiceSpec *compose.Service
}

// Deployer implemente le comportement propre a un type d'artefact.
type Deployer interface {
	Type() config.Type
	PlanDeploy(*Context) ([]Step, error)
	PlanRollback(*Context) ([]Step, error)
	Status(*Context) ([]string, error)
}

var registry = map[config.Type]Deployer{}

func register(d Deployer) { registry[d.Type()] = d }

// For renvoie le Deployer correspondant au type declare.
func For(t config.Type) (Deployer, error) {
	d, ok := registry[t]
	if !ok {
		return nil, fmt.Errorf("aucun deployeur pour le type %q", t)
	}
	return d, nil
}

// Execute deroule un plan. En dry-run, les descriptions sont affichees et
// aucune action n'est lancee.
func Execute(steps []Step, log *ui.Logger) error {
	total := len(steps)
	for i, s := range steps {
		log.Step(i+1, total, s.Desc)
		if log.DryRun() || s.Run == nil {
			continue
		}
		if err := s.Run(); err != nil {
			return fmt.Errorf("etape %d/%d (%s) : %w", i+1, total, s.Desc, err)
		}
	}
	return nil
}
