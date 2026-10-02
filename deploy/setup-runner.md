# Setup: self-hosted GitHub Actions runner (elo.ai only)

> **Nota:** este guia vive no repo `orchestrator` por conveniência
> histórica, mas hoje só se aplica ao repo **elo-ai**. Desde que o
> orchestrator passou a rodar localmente contra um clone local de
> `elo-ai` e a chegar no ambiente remoto só via `git push` -> CI/CD do elo.ai,
> o orchestrator não precisa de runner nenhum — ele nunca é buildado nem
> deployado na VM. Não existe mais `orchestrator/deploy/orchestrator.service`
> nem `orchestrator/.github/workflows/deploy.yml` — foram removidos.

Guia manual — rode isto UMA VEZ na VM Ubuntu, depois que o repo
`elo-ai` existir no GitHub. Isto NÃO é executado automaticamente por
nenhuma ferramenta; são comandos para você rodar na VM.

O runner self-hosted puxa jobs via conexão de saída (outbound) da VM para o
GitHub — não é necessário abrir porta nenhuma nem expor SSH da VM para a
internet.

## 1. Pegar o token de registro do runner

No GitHub: `Settings` → `Actions` → `Runners` → `New self-hosted runner`
(ou via `gh api repos/<owner>/elo-ai/actions/runners/registration-token`).
Copie a URL do repo e o token mostrados na página — eles são usados no
passo de configuração abaixo.

## 2. Instalar o runner na VM

```bash
mkdir -p ~/actions-runner-elo-ai && cd ~/actions-runner-elo-ai

# Confira a versão mais recente em https://github.com/actions/runner/releases
curl -o actions-runner-linux-x64.tar.gz -L \
  https://github.com/actions/runner/releases/download/v2.319.1/actions-runner-linux-x64-2.319.1.tar.gz

tar xzf actions-runner-linux-x64.tar.gz
```

## 3. Configurar o runner

```bash
./config.sh --url https://github.com/<owner>/elo-ai \
  --token <TOKEN_DO_PASSO_1> \
  --name vm-elo-ai \
  --labels self-hosted,linux \
  --unattended
```

O label `linux` (além do `self-hosted` padrão) é o que
`elo-ai/.github/workflows/deploy.yml` usa em
`runs-on: [self-hosted, linux]`.

## 4. Instalar como serviço systemd e iniciar

```bash
sudo ./svc.sh install
sudo ./svc.sh start
sudo ./svc.sh status
```

## Pré-requisitos na VM

- Docker + Docker Compose plugin instalados (o workflow builda as imagens
  backend/extractor/frontend e sobe com `docker compose up -d --build`).
- Node.js 20 (o workflow builda o frontend com `npm ci && npm run build`
  antes de gerar a imagem).
- O usuário que roda o runner precisa de permissão para `docker` (grupo
  `docker`) — sem isso os passos de build/`docker compose up` falham.
- Não é necessário instalar Go nem o Claude Code CLI na VM: ambos rodam
  apenas na sua máquina local, contra o clone local de `elo-ai`.
