# orchestrator

Loop autônomo em Go que orquestra duas invocações do [Claude Code
CLI](https://docs.claude.com/claude-code) (`claude -p`) — uma no papel de
Product Owner (PO), outra no papel de Desenvolvedor (Dev) — para levar um
backlog do zero até "produto pronto", sem intervenção humana a cada passo.
Toda a memória do processo (visão, estado, backlog, histórico e checkpoint
de execução) é persistida em disco, então o loop sobrevive a crashes do
processo, reinícios da máquina e aos rate limits da assinatura Claude
Pro/Max.

**O orchestrator roda localmente** (Windows via WSL2), contra um clone
local do repo alvo (`pdf-reader`) — ele nunca roda na VM. O único jeito de
código chegar na VM é: o PO aceita uma task → o orchestrator roda uma
verificação local → commit + push → o CI/CD do `pdf-reader` (self-hosted
runner na VM) builda e deploya. Veja "Arquitetura de deploy" abaixo.

Só usa a biblioteca padrão do Go — sem dependências externas.

## Arquitetura de deploy

```
orchestrator (local, WSL2)          VM Ubuntu
┌─────────────────────────┐         ┌──────────────────────────┐
│ loop PO/Dev              │  push   │ self-hosted runner        │
│  accept → verify → ──────┼────────▶│  pdf-reader CI/CD          │
│  git commit + push       │         │  docker build/compose up  │
└─────────────────────────┘         └──────────────────────────┘
```

O orchestrator em si NÃO tem mais workflow de deploy nem systemd unit —
eles existiam quando o orchestrator rodava na VM e foram removidos (ver
`deploy/setup-runner.md`, que hoje documenta o runner só para o repo
`pdf-reader`). Ele lê e escreve arquivos em `ORCH_REPO_DIR`, que deve
apontar para o seu clone LOCAL de `pdf-reader` (ex:
`~/projects/pdf-reader/backend`), não para um caminho na VM.

## Guardrail: nunca dar push de código quebrado

Antes de cada commit+push, se `ORCH_LOCAL_VERIFY_COMMAND` estiver
configurado (ex: `go build ./... && go vet ./...`), o orchestrator roda
esse comando em `ORCH_REPO_DIR` via `sh -c`. Se falhar:

- Não faz commit nem push.
- Trata como um reject automático da task — sem gastar uma chamada de API
  no PO para essa decisão: incrementa `AttemptsOnTask`, gera um
  correction_prompt determinístico com a saída de erro do comando, e volta
  o Dev pra tentar de novo. Depois de `MaxAttemptsPerTask` falhas (verify
  ou PO reject, contam juntos), a task vai pra `blocked`.

Se o `git push` em si falhar (conflito, branch protegida, sem
credenciais), o orchestrator NÃO tenta resolver sozinho: marca a task
como `blocked`, notifica com o erro completo do git, e para. Push é
sempre feito pra `origin <branch atual>` — configure a branch/remote do
seu clone local antes de rodar o loop.

## Por que `--dangerously-skip-permissions` é seguro neste contexto específico

O adapter `claudecli` sempre passa `--dangerously-skip-permissions` nas
chamadas ao Dev (nunca nas chamadas ao PO — o PO não toca em arquivos, e
mantém os prompts de permissão normais do Claude Code). Motivo: `claude -p`
roda sem TTY (modo headless) — se uma tool pedir aprovação interativa, não
tem quem responda, e a chamada simplesmente trava para sempre. Sem essa
flag, o Dev nunca consegue completar uma task que edite/crie arquivos.

Isso remove uma camada de segurança real do Claude Code (ele para de pedir
aprovação antes de qualquer ação). É aceitável especificamente aqui porque:

- **(a) `AllowedTools` já restringe explicitamente** o que o Dev pode
  fazer — `--dangerously-skip-permissions` pula a pergunta "posso usar
  X?", mas não amplia o conjunto de tools disponíveis; ferramentas fora de
  `ORCH_DEV_ALLOWED_TOOLS` continuam indisponíveis.
- **(b) `WorkDir` é sempre um diretório de projeto específico**
  (`ORCH_REPO_DIR`), nunca o sistema inteiro — o blast radius de qualquer
  ação do Dev fica contido ali.
- **(c) O guardrail de `LocalVerifyCommand` + `GitPusher`** (ver acima)
  impede que código quebrado seja commitado/pushado mesmo que o Dev faça
  algo errado sob essa flag — nada sai do `WorkDir` local sem passar pela
  verificação primeiro.

Se esse raciocínio deixar de valer (ex: `ORCH_DEV_ALLOWED_TOOLS` vazio,
`ORCH_REPO_DIR` apontando pra um lugar sensível, ou `LocalVerifyCommand`
desabilitado), reavalie usar essa flag antes de rodar o loop.

## Por que isso existe: rate limit da assinatura

O orchestrator autentica no Claude Code via `CLAUDE_CODE_OAUTH_TOKEN`, ou
seja, usa os limites de uso de uma assinatura Claude Pro/Max, não uma API
key paga por token. Assinaturas têm uma janela de uso (tipicamente de
algumas horas) depois da qual novas chamadas são recusadas até a janela
resetar. Rodando de forma autônoma por horas ou dias, o orchestrator VAI
bater nesse limite — isso é esperado, não é um erro do sistema.

Quando isso acontece:

1. O adapter `claudecli` detecta o rate limit checando se a saída
   (stdout+stderr) do `claude -p` contém uma das substrings listadas em
   `RateLimitIndicators` (`internal/adapters/claudecli/runner.go`).
   **Essa lista é um palpite educado baseado no texto usado pelo Claude
   Code no momento em que este código foi escrito — o texto exato não é
   uma API estável e pode mudar entre versões do CLI.** Depois do primeiro
   rate limit real, confira a saída bruta que foi logada e ajuste a lista
   manualmente se ela não bater mais.
2. O loop NÃO trata isso como erro fatal. Ele salva `RateLimitedAt` no
   checkpoint (só na primeira vez), notifica, e espera com backoff
   exponencial: começa em `RateLimitWaitMin` (padrão 10min), dobra a cada
   rodada até `RateLimitWaitMax` (padrão 90min).
3. Depois de esperar, tenta de novo a MESMA chamada — nenhuma fase é
   perdida, nenhum progresso é descartado.
4. Se o processo for reiniciado (reboot da máquina, crash, você matou o
   processo e subiu de novo) enquanto está esperando, ele recalcula o
   tempo restante a partir do `RateLimitedAt` já salvo em disco, em vez de
   recomeçar a espera do zero.

## Compilando

Requer Go 1.21+. O binário é o mesmo que você roda localmente (WSL2 já é
linux/amd64):

```bash
GOOS=linux GOARCH=amd64 go build -o bin/orchestrator ./cmd/orchestrator
```

Rodar os testes:

```bash
go test ./...
go vet ./...
```

## Autenticação: `claude setup-token`

O orchestrator não guarda nem gerencia credenciais — ele só invoca o
binário `claude` já autenticado no ambiente. Na máquina onde o
orchestrator vai rodar (seu WSL2), autentique uma vez com a assinatura
Pro/Max:

```bash
claude setup-token
```

Isso abre um fluxo OAuth e imprime um token de longa duração. Exporte
esse valor como `CLAUDE_CODE_OAUTH_TOKEN` no ambiente do processo (ver
`deploy/orchestrator.env.example`). Não é uma API key: é um token
vinculado à sua assinatura, então o uso é contado contra os limites do
plano Pro/Max, não cobrado por token.

Da mesma forma, `git push` precisa já funcionar manualmente na sua
máquina (credenciais/SSH key configuradas) ANTES de rodar o loop — o
adapter `gitcli` não gerencia autenticação, só chama `git`.

## Preparando `data/` para um projeto novo

O orchestrator lê e escreve tudo sob `ORCH_DATA_DIR`. Para apontar o
orchestrator para um novo projeto (ex: pdf-reader):

```bash
mkdir -p /caminho/para/data
cp templates/vision.md /caminho/para/data/vision.md
cp templates/backlog.json /caminho/para/data/backlog.json
```

Depois:

1. **Edite `vision.md` manualmente** — esta é a única etapa que exige um
   humano antes do primeiro ciclo. Preencha O que é / Fora de escopo /
   Convenções arquiteturais obrigatórias / Critério de "produto pronto".
2. **Edite `backlog.json`** com as tasks iniciais reais (apague a task de
   exemplo).
3. Não crie `state.md` nem `run_state.json` manualmente — o orchestrator
   os cria sozinho no primeiro ciclo (estado vazio = fase `idle`, que o
   loop trata como o início de um ciclo `po_deciding`).

Layout resultante:

```
data/
├── vision.md          # só o humano escreve
├── state.md            # PO reescreve a cada avaliação (sempre a versão completa)
├── backlog.json         # tasks, atualizadas pelo loop
├── run_state.json        # checkpoint de execução (fase atual, tentativas, rate limit)
└── history/
    └── <task-id>.md      # log append-only de cada avaliação daquela task (inclui resultado de verify/push)
```

## Variáveis de ambiente

| Variável | Obrigatória | Default | Descrição |
|---|---|---|---|
| `CLAUDE_CODE_OAUTH_TOKEN` | sim | — | Token de `claude setup-token`. Lido pelo próprio binário `claude`, não pelo orchestrator. |
| `ORCH_REPO_DIR` | não | `.` | Diretório de trabalho onde o Dev/PO rodam E onde o commit+push acontece — o seu clone LOCAL de `pdf-reader/backend` (ou outro subdiretório, conforme o backlog avança). |
| `ORCH_DATA_DIR` | não | `./data` | Raiz da memória persistida (vision/state/backlog/history/run_state). |
| `ORCH_MAX_ATTEMPTS_PER_TASK` | não | `3` | Quantas rejeições seguidas (do PO OU do LocalVerifyCommand) antes de marcar a task como `blocked` e parar. |
| `ORCH_MAX_TASKS_PER_CYCLE` | não | `0` (sem limite) | Se >0, o processo para de pegar tasks novas após completar esse número num único `Run`, mantendo a fase em `po_deciding` para retomar depois. |
| `ORCH_DEV_ALLOWED_TOOLS` | não | (vazio = política padrão do CLI) | Lista separada por vírgula de tools permitidas ao Dev (ex: `Bash,Edit,Read,Write`). Recomendado sempre definir. |
| `ORCH_LOCAL_VERIFY_COMMAND` | não | (vazio = pula a verificação, não recomendado) | Comando rodado em `ORCH_REPO_DIR` antes de todo commit+push — via `cmd /C` no Windows, via `sh -c` em outros sistemas (`buildVerifyCommand` em `loop.go` decide isso a partir de `runtime.GOOS`, sem depender de `sh.exe` estar no PATH do Windows, que frequentemente não está mesmo com Git instalado). Ex: `go build ./... && go vet ./...` quando `ORCH_REPO_DIR` for `pdf-reader/backend`; ajuste para `npm run build` (frontend) ou o equivalente do extractor conforme o backlog cobrir outras partes do repo. **O comando precisa usar sintaxe compatível com o interpretador do SO onde o orquestrador roda** — `cmd.exe` no Windows tem sutilezas diferentes de `sh` (ex: encadeamento com `&&` funciona nos dois, mas redirecionamento, aspas e variáveis de ambiente têm sintaxes distintas). Teste o comando manualmente no terminal (PowerShell/cmd no Windows, bash/sh em outros sistemas) antes de configurá-lo via env var. |
| `ORCH_GIT_COMMIT_PREFIX` | não | `feat: ` | Prefixo da mensagem de commit gerada (`<prefixo><título da task>\n\n<reasoning do PO>`). |
| `ORCH_PO_MODEL` | não | (default do CLI) | Modelo usado nas chamadas do PO. |
| `ORCH_DEV_MODEL` | não | (default do CLI) | Modelo usado nas chamadas do Dev. |
| `ORCH_NOTIFY_WEBHOOK` | não | (vazio = notificações vão pro stdout/log) | URL que recebe `POST {"message": "..."}` a cada notificação (rate limit, task bloqueada, push falhou, etc). Pense em plugar aqui um webhook de WhatsApp. |

`DevMaxTurns` (padrão 30) e as janelas de espera de rate limit
(`RateLimitWaitMin`/`RateLimitWaitMax`, padrão 10min/90min) usam os
defaults de `internal/orchestrator/loop.go` — não há env var para eles
ainda; ajuste o código se precisar mudar.

## Rodando

```bash
export CLAUDE_CODE_OAUTH_TOKEN=...
export ORCH_REPO_DIR=~/projects/pdf-reader/backend
export ORCH_DATA_DIR=~/projects/orchestrator/data
export ORCH_DEV_ALLOWED_TOOLS="Bash,Edit,Read,Write,Glob,Grep"
export ORCH_LOCAL_VERIFY_COMMAND="go build ./... && go vet ./..."
./bin/orchestrator
```

`Ctrl+C` (SIGINT) ou `kill` (SIGTERM) fazem o processo sair depois de
terminar a operação em andamento com segurança — o checkpoint em
`run_state.json` já reflete o progresso mais recente antes de qualquer
chamada arriscada ser feita, então nada é perdido.

## Arquitetura

Hexagonal: `internal/domain` define os tipos puros (Phase, Task,
DevReport, PODecision, POEvaluation, RunState); `internal/ports` declara
as interfaces (`AgentRunner`, `MemoryStore`, `Notifier`, `GitPusher`);
`internal/adapters/*` implementa essas interfaces contra o mundo real
(`claude -p` via os/exec, arquivos em disco, stdout/webhook, `git` via
os/exec); `internal/orchestrator` contém a state machine que orquestra
tudo, incluindo o guardrail de verify+push no accept, sem depender de
nenhum detalhe concreto de adapter.
