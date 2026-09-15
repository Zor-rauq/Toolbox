// Commande toolbox : construit un composant et depose son artefact sur une VM
// de developpement.
//
// Cet outil s'adresse exclusivement a la boucle de developpement. Il refuse
// toute cible qui ne declare pas env: dev.
package main

import (
	"flag"
	"fmt"
	"os"

	"toolbox/internal/artifact"
	"toolbox/internal/compose"
	"toolbox/internal/config"
	"toolbox/internal/deploy"
	"toolbox/internal/ssh"
	"toolbox/internal/ui"
)

const usage = `toolbox - build et depot d'artefacts sur une VM de developpement

Usage:
  toolbox deploy   --target <cible> [options]
  toolbox rollback --target <cible> [options]
  toolbox status   --target <cible> [options]

Options communes:
  --target       nom de la cible dans le fichier de cibles (obligatoire)
  --config       descripteur du composant (defaut: ./deploy.yaml)
  --targets      fichier de cibles (defaut: ~/.config/toolbox/targets.yaml)
  --dry-run      lit l'etat de la VM et affiche le plan sans rien ecrire
  --verbose      sortie detaillee

Surcharges (priorite: flag > variable d'environnement > descripteur):
  --component, --type, --service, --version, --artifact, --user

Options de deploy:
  --no-build     ne lance pas le script de build, l'artefact existe deja
`

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "\nErreur: %v\n", err)
		os.Exit(1)
	}
}

type opts struct {
	target      string
	configPath  string
	targetsPath string
	dryRun      bool
	verbose     bool
	noBuild     bool
	ov          config.Overrides
}

func run() error {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		return fmt.Errorf("commande manquante")
	}
	cmd := os.Args[1]
	switch cmd {
	case "-h", "--help", "help":
		fmt.Print(usage)
		return nil
	case "deploy", "rollback", "status":
	default:
		fmt.Print(usage)
		return fmt.Errorf("commande inconnue : %q", cmd)
	}

	o := &opts{}
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	fs.StringVar(&o.target, "target", os.Getenv("TOOLBOX_TARGET"), "cible")
	fs.StringVar(&o.configPath, "config", "deploy.yaml", "descripteur du composant")
	fs.StringVar(&o.targetsPath, "targets", config.DefaultTargetsPath(), "fichier de cibles")
	fs.BoolVar(&o.dryRun, "dry-run", false, "afficher le plan sans rien ecrire")
	fs.BoolVar(&o.verbose, "verbose", false, "sortie detaillee")
	fs.StringVar(&o.ov.Component, "component", "", "surcharge du nom de composant")
	fs.StringVar(&o.ov.Type, "type", "", "surcharge du type d'artefact")
	fs.StringVar(&o.ov.Service, "service", "", "surcharge du service compose")
	fs.StringVar(&o.ov.Version, "version", "", "surcharge de la version")
	fs.StringVar(&o.ov.Artifact, "artifact", "", "surcharge du chemin d'artefact")
	fs.StringVar(&o.ov.User, "user", "", "surcharge de l'utilisateur SSH")
	if cmd == "deploy" {
		fs.BoolVar(&o.noBuild, "no-build", false, "ne pas lancer le script de build")
	}
	if err := fs.Parse(os.Args[2:]); err != nil {
		return err
	}

	log := ui.New(o.verbose, o.dryRun)

	comp, err := config.LoadComponent(o.configPath, o.ov)
	if err != nil {
		return err
	}
	target, err := config.LoadTarget(o.targetsPath, o.target, o.ov)
	if err != nil {
		return err
	}
	dep, err := deploy.For(comp.Type)
	if err != nil {
		return err
	}
	log.Info("composant : %s (%s) -> cible %s [%s@%s]",
		comp.Name, comp.Type, target.Name(), target.User, target.Host)

	// Le build est local et precede toute connexion : inutile de demander le
	// mot de passe si la construction echoue.
	var art *artifact.Artifact
	if cmd == "deploy" {
		if !o.noBuild && !o.dryRun {
			if err := artifact.Build(comp, log); err != nil {
				return err
			}
		} else if o.dryRun && !o.noBuild {
			log.Info("dry-run : le script de build n'est pas lance")
		}
		art, err = artifact.Resolve(comp, log)
		if err != nil {
			return err
		}
	}

	client, err := ssh.Dial(ssh.Options{
		Addr:       target.Addr(),
		User:       target.User,
		KnownHosts: target.KnownHosts,
	}, ssh.NewPrompt())
	if err != nil {
		return err
	}
	defer client.Close()
	log.Debug("connecte, HOME distant : %s", client.Home())

	// Le format de configuration accepte deja une liste, en prevision de la
	// decomposition par silo. Tant qu'elle n'est pas traitee, on le dit.
	if len(target.ComposeFiles) > 1 {
		return fmt.Errorf(
			"cible %s : %d fichiers compose declares, cette version n'en traite qu'un",
			target.Name(), len(target.ComposeFiles))
	}
	cf, err := compose.Load(client, target.ComposeFiles[0])
	if err != nil {
		return err
	}

	ctx := &deploy.Context{
		Comp:     comp,
		Target:   target,
		SSH:      client,
		Compose:  cf,
		Log:      log,
		DryRun:   o.dryRun,
		Artifact: art,
	}

	switch cmd {
	case "status":
		lines, err := dep.Status(ctx)
		if err != nil {
			return err
		}
		fmt.Println()
		for _, l := range lines {
			fmt.Println("  " + l)
		}
		return nil

	case "deploy":
		steps, err := dep.PlanDeploy(ctx)
		if err != nil {
			return err
		}
		fmt.Println()
		if err := deploy.Execute(steps, log); err != nil {
			return err
		}
		if !o.dryRun {
			log.Info("deploiement termine")
		}
		return nil

	case "rollback":
		steps, err := dep.PlanRollback(ctx)
		if err != nil {
			return err
		}
		fmt.Println()
		if err := deploy.Execute(steps, log); err != nil {
			return err
		}
		if !o.dryRun {
			log.Info("retour arriere termine")
		}
		return nil
	}
	return nil
}
