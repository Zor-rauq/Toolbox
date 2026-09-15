# toolbox

Construit un composant et depose son artefact sur une VM de developpement.

Cet outil s'adresse **exclusivement a la boucle de developpement**. Il refuse
toute cible qui ne declare pas `env: dev`. Les autres environnements passent par
la chaine de livraison.

## Etat

| Type d'artefact | Etat |
|---|---|
| `quarkus-image` | implemente |
| `ear-war` | a venir |
| `quarkus-zip` | a venir |

## Construction

Le conteneur ayant servi a ecrire ce code n'avait ni Go ni acces reseau : rien
n'a ete compile. Premiere etape de votre cote :

```sh
go mod tidy    # resout les dependances via votre GOPROXY (Nexus)
go build ./...
go vet ./...
```

Quatre dependances, toutes courantes et deja largement mirroir-ees :
`golang.org/x/crypto` (SSH), `golang.org/x/term` (saisie sans echo),
`github.com/pkg/sftp` (transfert sur le meme canal), `gopkg.in/yaml.v3`.

Installation :

```sh
go build -o ~/.local/bin/toolbox ./cmd/toolbox
```

## Renommer le module

Le module s'appelle `toolbox`. Pour le publier sous un chemin interne :

```sh
go mod edit -module git.interne/equipe/toolbox
grep -rl '"toolbox/internal' --include='*.go' . \
  | xargs sed -i 's|"toolbox/internal|"git.interne/equipe/toolbox/internal|g'
go build ./...
```

Deux endroits sont concernes, et rien d'autre : la directive `module` de
`go.mod`, et les imports internes prefixes `toolbox/internal/`.

## Configuration

### `deploy.yaml`, versionne dans le depot du composant

```yaml
component: mon-service
type: quarkus-image          # ear-war | quarkus-zip | quarkus-image

build:
  script: ./build.sh         # defaut
  env:
    MAVEN_OPTS: -Xmx2g

artifact:
  path: target/*-image.tar   # motif glob, doit correspondre a un seul fichier
  manifest: .toolbox/artifact.json   # optionnel, prime sur path s'il existe

# Facultatifs, deduits sinon
service: mon-service         # deduit du compose distant
version: ""                  # deduite de git describe
image:
  source_ref: ""             # etiquette portee par l'image dans l'archive
```

### `~/.config/toolbox/targets.yaml`, hors depot

```yaml
targets:
  dev1:
    host: vm-dev-01
    port: 22
    user: monuser
    env: dev                 # obligatoire, seule valeur acceptee
    compose_files:
      - $HOME/current        # fichier ou repertoire
    compose_bin: podman-compose
    podman_bin: podman
    wildfly_deployments: $HOME/current/data/wildfly_data/deployments
```

`$HOME` est developpe **sur la VM**, jamais localement.

### Manifeste optionnel

Un script de build peut lever une ambiguite en ecrivant `.toolbox/artifact.json` :

```json
{ "path": "target/mon-service-image.tar", "version": "1.4.2" }
```

## Utilisation

```sh
toolbox deploy   --target dev1
toolbox deploy   --target dev1 --no-build     # artefact deja construit
toolbox deploy   --target dev1 --dry-run      # lit la VM, n'ecrit rien
toolbox rollback --target dev1
toolbox status   --target dev1
```

Cascade de resolution, du plus fort au plus faible :
flag CLI, variable d'environnement (`TOOLBOX_COMPONENT`, `TOOLBOX_TYPE`,
`TOOLBOX_SERVICE`, `TOOLBOX_VERSION`, `TOOLBOX_ARTIFACT_PATH`, `TOOLBOX_USER`,
`TOOLBOX_TARGET`, `TOOLBOX_TARGETS`), descripteur, deduction, puis echec
explicite. Jamais de repli silencieux.

## Principes de conception

**Une seule connexion SSH par execution.** Le mot de passe est saisi une fois,
sans echo, et sert aux commandes distantes comme aux transferts via sftp sur le
meme canal. Il n'est jamais ecrit sur disque ni passe en argument de commande,
ou il serait visible dans `ps`. L'empreinte du serveur est verifiee contre
`known_hosts`, sans exception.

Les deux methodes d'authentification par mot de passe sont proposees. Beaucoup
de serveurs configures en `PasswordAuthentication` repondent en realite en
`keyboard-interactive` via PAM, et n'accepter que `password` produit un echec
difficile a diagnostiquer.

**Le compose distant est la source de verite, et n'est jamais ecrit.** Il fournit
le nom du service, l'image attendue et les chemins de donnees. Pour le type
image, l'image chargee recoit l'etiquette que le compose attend deja, plutot que
de modifier le fichier.

**Un Plan ne fait que lire, seules les Steps ecrivent.** C'est ce qui rend
`--dry-run` honnete sans code dedie. En contrepartie, `--dry-run` ouvre bien une
connexion et demande le mot de passe.

**Rotation a une generation.** Pour le type image, l'image en place est etiquetee
`:previous` avant le basculement. Le retour arriere est alors un simple retag,
sans transfert. Chaque image chargee recoit en plus une etiquette derivee de la
version, ce qui permet de savoir ce qui tourne reellement meme apres plusieurs
deploiements.

**Transfert ignore si inutile.** L'empreinte de la derniere archive chargee est
memorisee dans `$HOME/.toolbox/<composant>.image.sha256` sur la VM. Si l'archive
locale est identique, les centaines de mega-octets ne repartent pas.

## Ajouter un type d'artefact

Un fichier dans `internal/deploy/`, une implementation de `Deployer`, un
`register()` dans son `init()`. Rien d'autre ne bouge. Les briques reutilisables
sont dans `pipeline.go` : transfert, verification d'empreinte, recreation de
service, marqueurs.

## Points a valider sur la VM

1. La commande `podman-compose` gere la sequence `stop` / `rm -f` / `up -d`
   restreinte a un service. Un `down` restreint a un service n'est pas fiable
   d'une version a l'autre, d'ou ce choix.
2. Le format de sortie de `podman load`. Si la reference n'est pas reconnue, le
   message d'erreur le dit et `image.source_ref` permet de la declarer.
3. Le libelle exact du label utilise par `podman-compose` pour `status`
   (`io.podman.compose.service`), qui peut varier selon la version.
   
