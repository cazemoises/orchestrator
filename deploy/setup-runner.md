# Setup: self-hosted GitHub Actions runner (pdf-reader only)

> **Nota:** este guia vive no repo `orchestrator` por conveniência
> histórica, mas hoje só se aplica ao repo **pdf-reader**. Desde que o
> orchestrator passou a rodar localmente (WSL2, contra um clone local de
> `pdf-reader`) e a chegar na VM só via `git push` -> CI/CD do pdf-reader,
> o orchestrator não precisa de runner nenhum — ele nunca é buildado nem
> deployado na VM. Não existe mais `orchestrator/deploy/orchestrator.service`
> nem `orchestrator/.github/workflows/deploy.yml` — foram removidos.

Guia manual — rode isto UMA VEZ na VM Ubuntu, depois que o repo
`pdf-reader` existir no GitHub. Isto NÃO é executado automaticamente por
nenhuma ferramenta; são comandos para você rodar na VM.

O runner self-hosted puxa jobs via conexão de saída (outbound) da VM para o
GitHub — não é necessário abrir porta nenhuma nem expor SSH da VM para a
internet.

## 1. Pegar o token de registro do runner

No GitHub: `Settings` → `Actions` → `Runners` → `New self-hosted runner`
(ou via `gh api repos/<owner>/pdf-reader/actions/runners/registration-token`).
Copie a URL do repo e o token mostrados na página — eles são usados no
passo de configuração abaixo.

## 2. Instalar o runner na VM

```bash
mkdir -p ~/actions-runner-pdf-reader && cd ~/actions-runner-pdf-reader

# Confira a versão mais recente em https://github.com/actions/runner/releases
curl -o actions-runner-linux-x64.tar.gz -L \
  https://github.com/actions/runner/releases/download/v2.319.1/actions-runner-linux-x64-2.319.1.tar.gz

tar xzf actions-runner-linux-x64.tar.gz
```

## 3. Configurar o runner

```bash
./config.sh --url https://github.com/<owner>/pdf-reader \
  --token <TOKEN_DO_PASSO_1> \
  --name vm-pdf-reader \
  --labels self-hosted,linux \
  --unattended
```

O label `linux` (além do `self-hosted` padrão) é o que
`pdf-reader/.github/workflows/deploy.yml` usa em
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
- Não é necessário instalar Go nem o Claude Code CLI na VM: nenhum dos
  dois roda mais aqui. O orchestrator (Go) e o `claude` CLI só rodam na
  sua máquina local, contra o clone local de `pdf-reader`.
