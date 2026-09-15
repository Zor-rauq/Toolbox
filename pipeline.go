package deploy

import (
	"fmt"
	"path"
	"strings"

	"toolbox/internal/ssh"
)

// workDir est le repertoire de travail de la toolbox sur la VM.
const workDir = "$HOME/.toolbox"

// ResolveService renseigne le service et verifie la politique de pull.
func ResolveService(ctx *Context) error {
	name, svc, err := ctx.Compose.FindService(ctx.Comp.Service, ctx.Comp.Name)
	if err != nil {
		return err
	}
	if err := svc.CheckPullPolicy(); err != nil {
		return fmt.Errorf("service %s : %w", name, err)
	}
	ctx.Service = name
	ctx.ServiceSpec = svc
	ctx.Log.Info("service : %s (compose %s)", name, ctx.Compose.Path)
	return nil
}

// StagingPath renvoie l'emplacement temporaire d'un fichier transfere.
func StagingPath(name string) string { return path.Join(workDir, "staging", name) }

// MarkerPath renvoie le fichier qui memorise l'empreinte deployee.
func MarkerPath(component, kind string) string {
	return path.Join(workDir, fmt.Sprintf("%s.%s.sha256", component, kind))
}

// StepUpload transfere un fichier local vers la VM.
func StepUpload(ctx *Context, local, remote string) Step {
	return Step{
		Desc: fmt.Sprintf("Transferer %s vers %s", path.Base(local), remote),
		Run:  func() error { return ctx.SSH.Upload(local, remote) },
	}
}

// StepVerifyChecksum recalcule l'empreinte cote VM et la compare.
// Un transfert tronque est ainsi detecte avant toute action destructrice.
func StepVerifyChecksum(ctx *Context, remote, want string) Step {
	return Step{
		Desc: "Verifier l'empreinte du fichier transfere",
		Run: func() error {
			out, err := ctx.SSH.Run("sha256sum " + ssh.Quote(ctx.SSH.Expand(remote)))
			if err != nil {
				return err
			}
			got := strings.Fields(out)
			if len(got) == 0 {
				return fmt.Errorf("sha256sum n'a rien renvoye pour %s", remote)
			}
			if got[0] != want {
				return fmt.Errorf("empreinte differente apres transfert (attendu %s, obtenu %s)", want, got[0])
			}
			return nil
		},
	}
}

// StepRemoveRemote supprime un fichier temporaire sur la VM.
func StepRemoveRemote(ctx *Context, remote string) Step {
	return Step{
		Desc: fmt.Sprintf("Supprimer le fichier temporaire %s", path.Base(remote)),
		Run: func() error {
			if err := ctx.SSH.Remove(remote); err != nil {
				// Un temporaire residuel ne doit pas faire echouer un deploiement reussi.
				ctx.Log.Warn("suppression de %s impossible : %v", remote, err)
			}
			return nil
		},
	}
}

// StepWriteMarker memorise l'empreinte effectivement deployee.
func StepWriteMarker(ctx *Context, remote, sum string) Step {
	return Step{
		Desc: "Enregistrer l'empreinte deployee",
		Run:  func() error { return ctx.SSH.WriteFile(remote, []byte(sum+"\n")) },
	}
}

// StepClearMarker efface l'empreinte memorisee, apres un retour arriere.
func StepClearMarker(ctx *Context, remote string) Step {
	return Step{
		Desc: "Effacer l'empreinte memorisee",
		Run: func() error {
			if ctx.SSH.Exists(remote) {
				return ctx.SSH.Remove(remote)
			}
			return nil
		},
	}
}

// ReadMarker lit l'empreinte memorisee. Lecture seule, utilisable depuis un Plan.
func ReadMarker(ctx *Context, remote string) string {
	if !ctx.SSH.Exists(remote) {
		return ""
	}
	raw, err := ctx.SSH.ReadFile(remote)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// composeCmd construit une commande compose executee depuis le repertoire du
// fichier, ce dont podman-compose a besoin pour deduire le nom de projet et
// resoudre les chemins relatifs.
func composeCmd(ctx *Context, args ...string) string {
	parts := []string{ctx.Target.ComposeBin, "-f", ssh.Quote(ctx.Compose.Path)}
	parts = append(parts, args...)
	return "cd " + ssh.Quote(ctx.Compose.Dir) + " && " + strings.Join(parts, " ")
}

// StepRecreateService arrete, supprime puis relance le seul service vise.
//
// Un "down" restreint a un service n'est pas fiable d'une version de
// podman-compose a l'autre : la sequence stop / rm / up est equivalente et
// fonctionne partout, tout en laissant les autres services en place.
func StepRecreateService(ctx *Context) Step {
	svc := ssh.Quote(ctx.Service)
	return Step{
		Desc: fmt.Sprintf("Recreer le service %s", ctx.Service),
		Run: func() error {
			if _, err := ctx.SSH.Run(composeCmd(ctx, "stop", svc)); err != nil {
				ctx.Log.Warn("arret de %s : %v", ctx.Service, err)
			}
			if _, err := ctx.SSH.Run(composeCmd(ctx, "rm", "-f", svc)); err != nil {
				ctx.Log.Warn("suppression du conteneur %s : %v", ctx.Service, err)
			}
			_, err := ctx.SSH.Run(composeCmd(ctx, "up", "-d", svc))
			return err
		},
	}
}

// StepStopService arrete le service sans le supprimer.
func StepStopService(ctx *Context) Step {
	svc := ssh.Quote(ctx.Service)
	return Step{
		Desc: fmt.Sprintf("Arreter le service %s", ctx.Service),
		Run: func() error {
			_, err := ctx.SSH.Run(composeCmd(ctx, "stop", svc))
			return err
		},
	}
}

// ServiceState renvoie une description courte de l'etat du service.
// Lecture seule.
func ServiceState(ctx *Context) string {
	out, err := ctx.SSH.Run(fmt.Sprintf(
		"%s ps --filter label=io.podman.compose.service=%s --format '{{.Status}}'",
		ctx.Target.PodmanBin, ssh.Quote(ctx.Service)))
	if err != nil || strings.TrimSpace(out) == "" {
		return "arrete ou inconnu"
	}
	return strings.TrimSpace(strings.Split(out, "\n")[0])
}
