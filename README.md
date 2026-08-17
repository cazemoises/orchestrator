# orchestrator

Loop autônomo em Go que orquestra duas invocações do [Claude Code
CLI](https://docs.claude.com/claude-code) (`claude -p`) — uma no papel de
Product Owner (PO), outra no papel de Desenvolvedor (Dev) — para levar um
backlog do zero até "produto pronto", sem intervenção humana a cada passo.
Toda a memória do processo (visão, estado, backlog, histórico e checkpoint
de execução) é persistida em disco, então o loop sobrevive a reinícios da
VM, crashes do processo e aos rate limits da assinatura Claude Pro/Max.

Só usa a biblioteca padrão do Go — sem dependências externas.

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
   rate limit real em produção, confira a saída bruta que foi logada e
   ajuste a lista manualmente se ela não bater mais.
2. O loop NÃO trata isso como erro fatal. Ele salva `RateLimitedAt` no
   checkpoint (só na primeira vez), notifica, e espera com backoff
   exponencial: começa em `RateLimitWaitMin` (padrão 10min), dobra a cada
   rodada até `RateLimitWaitMax` (padrão 90min).
3. Depois de esperar, tenta de novo a MESMA chamada — nenhuma fase é
   perdida, nenhum progresso é descartado.
4. Se o processo for reiniciado (reboot da VM, `systemctl restart`, crash)
   enquanto está esperando, ele recalcula o tempo restante a partir do
   `RateLimitedAt` já salvo em disco, em vez de recomeçar a espera do
   zero. Isso é o que faz o systemd service (`Restart=on-failure`) ser
   seguro de usar aqui.

## Compilando

Requer Go 1.21+. Para rodar localmente (mesmo SO):

```bash
go build -o bin/orchestrator ./cmd/orchestrator
```

Para a VM Linux (build cross-platform, já que o desenvolvimento é feito no
Windows):

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
binário `claude` já autenticado no ambiente. Na VM (ou em qualquer máquina
onde o orchestrator vai rodar), autentique uma vez com a assinatura
Pro/Max:

```bash
claude setup-token
```

Isso abre um fluxo OAuth e imprime um token de longa duração. Guarde esse
valor como `CLAUDE_CODE_OAUTH_TOKEN` no ambiente do processo (ver
`deploy/orchestrator.service` — ele lê de `/etc/orchestrator.env`). Não é
uma API key: é um token vinculado à sua assinatura, então o uso é contado
contra os limites do plano Pro/Max, não cobrado por token.

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
    └── <task-id>.md      # log append-only de cada avaliação daquela task
```

## Variáveis de ambiente

| Variável | Obrigatória | Default | Descrição |
|---|---|---|---|
| `CLAUDE_CODE_OAUTH_TOKEN` | sim | — | Token de `claude setup-token`. Lido pelo próprio binário `claude`, não pelo orchestrator. |
| `ORCH_REPO_DIR` | não | `.` | Diretório de trabalho onde o Dev (e o PO) rodam — o repo do projeto alvo. |
| `ORCH_DATA_DIR` | não | `./data` | Raiz da memória persistida (vision/state/backlog/history/run_state). |
| `ORCH_MAX_ATTEMPTS_PER_TASK` | não | `3` | Quantas rejeições seguidas antes de marcar a task como `blocked` e parar. |
| `ORCH_MAX_TASKS_PER_CYCLE` | não | `0` (sem limite) | Se >0, o processo para de pegar tasks novas após completar esse número num único `Run`, mantendo a fase em `po_deciding` para retomar depois. Útil para forçar reinícios periódicos do processo. |
| `ORCH_DEV_ALLOWED_TOOLS` | não | (vazio = política padrão do CLI) | Lista separada por vírgula de tools permitidas ao Dev (ex: `Bash,Edit,Read,Write`). Recomendado sempre definir — sem isso o Dev pode não ter tools suficientes para editar código em modo não-interativo. |
| `ORCH_PO_MODEL` | não | (default do CLI) | Modelo usado nas chamadas do PO. |
| `ORCH_DEV_MODEL` | não | (default do CLI) | Modelo usado nas chamadas do Dev. |
| `ORCH_NOTIFY_WEBHOOK` | não | (vazio = notificações vão pro stdout/log) | URL que recebe `POST {"message": "..."}` a cada notificação (rate limit, task bloqueada, etc). Pense em plugar aqui um webhook de WhatsApp. |

`DevMaxTurns` (padrão 30) e as janelas de espera de rate limit
(`RateLimitWaitMin`/`RateLimitWaitMax`, padrão 10min/90min) usam os
defaults de `internal/orchestrator/loop.go` — não há env var para eles
ainda; ajuste o código se precisar mudar.

## Rodando

```bash
export CLAUDE_CODE_OAUTH_TOKEN=...
export ORCH_REPO_DIR=/caminho/para/o/projeto/alvo
export ORCH_DATA_DIR=/caminho/para/data
export ORCH_DEV_ALLOWED_TOOLS="Bash,Edit,Read,Write,Glob,Grep"
./bin/orchestrator
```

`Ctrl+C` (SIGINT) ou `systemctl stop` (SIGTERM) fazem o processo sair
depois de terminar a operação em andamento com segurança — o checkpoint em
`run_state.json` já reflete o progresso mais recente antes de qualquer
chamada arriscada ser feita, então nada é perdido.

## Arquitetura

Hexagonal: `internal/domain` define os tipos puros (Phase, Task,
DevReport, PODecision, POEvaluation, RunState); `internal/ports` declara
as interfaces (`AgentRunner`, `MemoryStore`, `Notifier`);
`internal/adapters/*` implementa essas interfaces contra o mundo real
(`claude -p` via os/exec, arquivos em disco, stdout/webhook);
`internal/orchestrator` contém a state machine que orquestra tudo, sem
depender de nenhum detalhe concreto de adapter.
