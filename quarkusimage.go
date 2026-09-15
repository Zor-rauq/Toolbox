package deploy

import (
	"fmt"
	"regexp"
	"strings"

	"toolbox/internal/compose"
	"toolbox/internal/config"
	"toolbox/internal/ssh"
)

func init() { register(&quarkusImage{}) }

// quarkusImage deploie une image construite localement, transferee sous forme
// d'archive podman save puis chargee sur la VM.
//
// Le compose n'est jamais modifie : l'image chargee recoit l'etiquette que le
// compose attend deja. L'image precedente est etiquetee ":previous" avant le
// basculement, ce qui fait du retour arriere un simple retag, sans transfert.
// Une seconde etiquette derivee de la version reste posee sur chaque image
// chargee, pour savoir a tout moment ce qui tourne reellement.
type quarkusImage struct{}

func (q *quarkusImage) Type() config.Type { return config.TypeQuarkusImage }

// previousTag est l'etiquette du point de retour.
const previousTag = "previous"

func (q *quarkusImage) PlanDeploy(ctx *Context) ([]Step, error) {
	if err := ResolveService(ctx); err != nil {
		return nil, err
	}
	repo, tag, err := imageRef(ctx)
	if err != nil {
		return nil, err
	}
	podman := ctx.Target.PodmanBin
	target := repo + ":" + tag
	versionRef := repo + ":" + sanitizeTag(ctx.Artifact.Version)
	prevRef := repo + ":" + previousTag

	// Lecture seule : etat courant de la VM.
	currentID := imageID(ctx, target)
	marker := MarkerPath(ctx.Comp.Name, "image")
	alreadyDeployed := ReadMarker(ctx, marker) == ctx.Artifact.SHA256 && currentID != ""

	remoteArchive := StagingPath(fmt.Sprintf("%s-%s.tar", ctx.Comp.Name, sanitizeTag(ctx.Artifact.Version)))

	var steps []Step

	if currentID != "" {
		ctx.Log.Info("image en place : %s (%s)", target, short(currentID))
		steps = append(steps, Step{
			Desc: fmt.Sprintf("Marquer l'image en place comme point de retour (%s)", prevRef),
			Run: func() error {
				_, err := ctx.SSH.Run(fmt.Sprintf("%s tag %s %s",
					podman, ssh.Quote(currentID), ssh.Quote(prevRef)))
				return err
			},
		})
	} else {
		ctx.Log.Warn("aucune image ne porte %s : ce deploiement n'aura pas de point de retour", target)
	}

	if alreadyDeployed {
		// L'archive locale est deja celle chargee sur la VM : inutile de
		// retransferer plusieurs centaines de mega-octets.
		ctx.Log.Info("archive identique a celle deja chargee, transfert ignore")
	} else {
		// loadedRef est renseigne par l'etape de chargement et consomme par
		// l'etape d'etiquetage.
		loadedRef := ctx.Comp.Image.SourceRef

		steps = append(steps,
			StepUpload(ctx, ctx.Artifact.Path, remoteArchive),
			StepVerifyChecksum(ctx, remoteArchive, ctx.Artifact.SHA256),
			Step{
				Desc: "Charger l'image sur la VM",
				Run: func() error {
					out, err := ctx.SSH.Run(fmt.Sprintf("%s load -i %s",
						podman, ssh.Quote(ctx.SSH.Expand(remoteArchive))))
					if err != nil {
						return err
					}
					if loadedRef != "" {
						return nil
					}
					ref, err := parseLoadedRef(out)
					if err != nil {
						return err
					}
					loadedRef = ref
					return nil
				},
			},
			Step{
				Desc: fmt.Sprintf("Etiqueter l'image en %s et %s", target, versionRef),
				Run: func() error {
					if loadedRef == "" {
						return fmt.Errorf("reference de l'image chargee inconnue")
					}
					for _, ref := range []string{target, versionRef} {
						if _, err := ctx.SSH.Run(fmt.Sprintf("%s tag %s %s",
							podman, ssh.Quote(loadedRef), ssh.Quote(ref))); err != nil {
							return err
						}
					}
					return nil
				},
			},
		)
	}

	steps = append(steps,
		StepRecreateService(ctx),
		StepWriteMarker(ctx, marker, ctx.Artifact.SHA256),
		Step{
			Desc: "Purger les images sans etiquette",
			Run: func() error {
				if _, err := ctx.SSH.Run(podman + " image prune -f"); err != nil {
					ctx.Log.Warn("purge des images : %v", err)
				}
				return nil
			},
		},
	)
	if !alreadyDeployed {
		steps = append(steps, StepRemoveRemote(ctx, remoteArchive))
	}
	return steps, nil
}

func (q *quarkusImage) PlanRollback(ctx *Context) ([]Step, error) {
	if err := ResolveService(ctx); err != nil {
		return nil, err
	}
	repo, tag, err := imageRef(ctx)
	if err != nil {
		return nil, err
	}
	podman := ctx.Target.PodmanBin
	target := repo + ":" + tag
	prevRef := repo + ":" + previousTag

	prevID := imageID(ctx, prevRef)
	if prevID == "" {
		return nil, fmt.Errorf(
			"aucun point de retour : %s n'existe pas sur la VM. "+
				"Listez les etiquettes disponibles avec '%s images %s'", prevRef, podman, repo)
	}
	ctx.Log.Info("point de retour : %s (%s)", prevRef, short(prevID))

	return []Step{
		{
			Desc: fmt.Sprintf("Reporter %s sur %s", prevRef, target),
			Run: func() error {
				_, err := ctx.SSH.Run(fmt.Sprintf("%s tag %s %s",
					podman, ssh.Quote(prevID), ssh.Quote(target)))
				return err
			},
		},
		StepRecreateService(ctx),
		StepClearMarker(ctx, MarkerPath(ctx.Comp.Name, "image")),
	}, nil
}

func (q *quarkusImage) Status(ctx *Context) ([]string, error) {
	if err := ResolveService(ctx); err != nil {
		return nil, err
	}
	repo, tag, err := imageRef(ctx)
	if err != nil {
		return nil, err
	}
	target := repo + ":" + tag
	prevRef := repo + ":" + previousTag

	lines := []string{
		fmt.Sprintf("service      : %s (%s)", ctx.Service, ServiceState(ctx)),
		fmt.Sprintf("image        : %s -> %s", target, orNone(short(imageID(ctx, target)))),
		fmt.Sprintf("point retour : %s -> %s", prevRef, orNone(short(imageID(ctx, prevRef)))),
	}
	if tags, err := ctx.SSH.Run(fmt.Sprintf(
		"%s images --format '{{.Repository}}:{{.Tag}}' %s",
		ctx.Target.PodmanBin, ssh.Quote(repo))); err == nil {
		lines = append(lines, "etiquettes   : "+strings.Join(strings.Fields(tags), " "))
	}
	return lines, nil
}

// imageRef tire du compose le depot et l'etiquette attendus par le service.
func imageRef(ctx *Context) (string, string, error) {
	if ctx.ServiceSpec.Image == "" {
		return "", "", fmt.Errorf(
			"le service %s ne declare pas d'image dans le compose ; "+
				"la toolbox ne peut pas determiner l'etiquette a poser", ctx.Service)
	}
	repo, tag := compose.SplitImage(ctx.ServiceSpec.Image)
	return repo, tag, nil
}

// imageID renvoie l'identifiant de l'image portant une reference, ou "" si
// aucune ne la porte. Lecture seule.
func imageID(ctx *Context, ref string) string {
	out, err := ctx.SSH.Run(fmt.Sprintf("%s image inspect --format '{{.Id}}' %s 2>/dev/null",
		ctx.Target.PodmanBin, ssh.Quote(ref)))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// loadedRefRe couvre les formulations de podman load rencontrees selon les versions.
var loadedRefRe = regexp.MustCompile(`(?i)Loaded image(?:\(s\))?:\s*(\S+)`)

func parseLoadedRef(out string) (string, error) {
	m := loadedRefRe.FindStringSubmatch(out)
	if len(m) < 2 {
		return "", fmt.Errorf(
			"impossible de lire la reference chargee dans la sortie de podman load. "+
				"Declarez 'image.source_ref' dans deploy.yaml. Sortie obtenue : %s",
			strings.TrimSpace(out))
	}
	return strings.TrimSuffix(strings.Split(m[1], ",")[0], ","), nil
}

// sanitizeTag rend une version utilisable comme etiquette d'image.
func sanitizeTag(v string) string {
	r := strings.NewReplacer("/", "-", ":", "-", " ", "-", "+", "-")
	return strings.Trim(r.Replace(v), "-.")
}

func short(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func orNone(s string) string {
	if s == "" {
		return "absent"
	}
	return s
}
