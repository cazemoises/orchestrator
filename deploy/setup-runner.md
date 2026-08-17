# Setup: self-hosted GitHub Actions runner (orchestrator)

Guia manual — rode isto UMA VEZ na VM Ubuntu, depois que o repo
`orchestrator` existir no GitHub. Isto NÃO é executado automaticamente por
nenhuma ferramenta; são comandos para você rodar na VM.

O runner self-hosted puxa jobs via conexão de saída (outbound) da VM para o
GitHub — não é necessário abrir porta nenhuma nem expor SSH da VM para a
internet.

## 1. Pegar o token de registro do runner

No GitHub: `Settings` → `Actions` → `Runners` → `New self-hosted runner`
(ou via `gh api repos/<owner>/orchestrator/actions/runners/registration-token`).
Copie a URL do repo e o token mostrados na página — eles são usados no
passo de configuração abaixo.

## 2. Instalar o runner na VM

```bash
mkdir -p ~/actions-runner-orchestrator && cd ~/actions-runner-orchestrator

# Confira a versão mais recente em https://github.com/actions/runner/releases
curl -o actions-runner-linux-x64.tar.gz -L \
  https://github.com/actions/runner/releases/download/v2.319.1/actions-runner-linux-x64-2.319.1.tar.gz

tar xzf actions-runner-linux-x64.tar.gz
```

## 3. Configurar o runner

```bash
./config.sh --url https://github.com/<owner>/orchestrator \
  --token <TOKEN_DO_PASSO_1> \
  --name vm-orchestrator \
  --labels self-hosted,linux \
  --unattended
```

O label `linux` (além do `self-hosted` padrão) é o que o workflow
`.github/workflows/deploy.yml` usa em `runs-on: [self-hosted, linux]`.

## 4. Instalar como serviço systemd e iniciar

```bash
sudo ./svc.sh install
sudo ./svc.sh start
sudo ./svc.sh status
```

## 5. Repetir para o repo pdf-reader

O mesmo processo (passos 1-4) precisa ser repetido para o repo
`pdf-reader`, em um diretório separado (ex: `~/actions-runner-pdf-reader`),
já que cada runner se registra contra um repositório específico. Use o
mesmo label set `self-hosted,linux` — o workflow de deploy do pdf-reader
espera o mesmo.

## Pré-requisitos na VM (antes ou depois de instalar o runner)

- Go 1.21+ instalado e no PATH do usuário que roda o runner (para o deploy
  do orchestrator).
- Docker + Docker Compose plugin instalados (para o deploy do pdf-reader).
- `claude` (Claude Code CLI) instalado e autenticado via
  `claude setup-token` — necessário para o orchestrator funcionar, não para
  o runner em si.
- O usuário que roda o runner precisa de permissão `sudo` sem senha para os
  comandos usados no workflow (`systemctl stop/start orchestrator`,
  `install`) — configure isso em `/etc/sudoers.d/` se ainda não estiver.
- `/etc/orchestrator.env` criado a partir de
  `deploy/orchestrator.env.example` com os valores reais preenchidos
  (`CLAUDE_CODE_OAUTH_TOKEN`, `ORCH_REPO_DIR`, `ORCH_DATA_DIR`, etc).
- `deploy/orchestrator.service` copiado para
  `/etc/systemd/system/orchestrator.service` e habilitado:
  ```bash
  sudo cp deploy/orchestrator.service /etc/systemd/system/orchestrator.service
  sudo systemctl daemon-reload
  sudo systemctl enable orchestrator
  ```
  (o primeiro `systemctl start` pode ficar a cargo do workflow de deploy,
  mas o `enable` — pra sobreviver a reboot — é manual.)
