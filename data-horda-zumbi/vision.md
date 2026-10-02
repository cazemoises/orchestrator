# Visão do produto: horda-zumbi

## O que é
Jogo de sobrevivência a hordas de zumbis, cooperativo, multiplayer
via rede (cada jogador no seu próprio navegador, conectando à mesma
sala via um código/link compartilhado). Até 3 jogadores por sala.
Visão top-down 2D, controle via teclado (movimento) e mouse (mira/
ataque). Ondas de zumbis cada vez mais difíceis, objetivo é sobreviver
o máximo possível. Sem persistência - salas existem só enquanto
alguém está conectado, sem login, sem salvar progresso/recorde.

## Fora de escopo
- Persistência de qualquer tipo (banco de dados, salvar progresso,
  ranking/leaderboard)
- Mais de 3 jogadores por sala
- Autenticação/contas de usuário
- Dispositivos móveis/touch - só desktop com teclado e mouse por
  enquanto
- Chat de voz/texto entre jogadores

## Convenções arquiteturais obrigatórias
- Backend Go hexagonal: domain/ports/adapters, mesmo padrão de
  projetos anteriores (pdf-reader)
- Estado de sala em memória (map thread-safe), nunca banco de dados
- WebSocket via github.com/coder/websocket
- Frontend: React+TS+Vite, jogo em si renderizado via HTML5 Canvas
  (não componentes React) por performance - React só para telas de
  menu/lobby (criar sala, entrar em sala)
- TDD obrigatório no backend (RED antes de GREEN) para lógica de jogo
  (dano, vida, spawn de onda, movimento) - testes que não dependem de
  rede real podem ser unitários; qualquer coisa envolvendo WebSocket
  real deve ter ao menos um teste de integração
- Commits atômicos

## Critério de "produto pronto" (v1)
1. É possível criar uma sala e receber um código/link compartilhável
2. Outro jogador consegue entrar na mesma sala usando esse código,
   em outro navegador/aba
3. Todos os jogadores da sala veem os personagens de todo mundo se
   movendo em tempo real (sincronização básica funcionando)
4. Zumbis aparecem em ondas, se movem em direção ao jogador mais
   próximo
5. Jogadores conseguem atacar/atirar em zumbis, causando dano e
   eventualmente matando-os
6. Jogadores têm vida, perdem vida ao serem atingidos por zumbis, e
   o jogo termina (tela de "fim de jogo") quando todos os jogadores
   da sala morrem
7. Ondas aumentam de dificuldade (mais zumbis, ou zumbis mais fortes)
   a cada rodada sobrevivida
8. Testado manualmente por mim com pelo menos 2 abas/dispositivos
   reais jogando ao mesmo tempo, confirmando sincronização aceitável
   (pode ter alguma imprecisão de posição, mas o jogo deve ser
   jogável e cooperativo de fato)