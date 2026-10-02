# Visão do produto: elo.ai

## O que é

O elo.ai é uma plataforma SaaS B2B de educação com IA para escolas. O
Copiloto do Professor gera planos de aula, slides e exercícios estruturados,
enquanto o Tutor Adaptativo responde dúvidas usando apenas materiais da
própria escola e ajusta a dificuldade com base no desempenho do aluno.

A plataforma deve oferecer multi-tenancy com isolamento estrito, consentimento
e retenção compatíveis com a LGPD, auditoria completa das interações com IA e
integrações plugáveis com sistemas escolares.

## Fora de escopo

- Respostas do Tutor baseadas em conhecimento aberto fora dos materiais do tenant
- Mistura de dados, prompts, embeddings ou cache entre tenants
- Reinforcement learning no motor adaptativo; ele usa regras versionadas e estatística
- Chamada do Gemini diretamente pelo frontend
- Gravação de conteúdo gerado sem validação de schema e sem autoria final do professor
- Dados de menores sem consentimento rastreável e política de retenção

## Convenções arquiteturais obrigatórias

- Backend em Go, frontend em React + TypeScript e PostgreSQL com pgvector.
- Manter o core isolado por ports/adapters; Gemini, retrieval e SIS nunca são
  dependências diretas do domínio.
- Toda tabela de negócio deve carregar `tenant_id` e usar Row-Level Security.
- Toda interação com IA deve ser registrada de forma append-only, incluindo
  prompt, resposta, modelo, tokens e fontes de RAG.
- Respeitar a arquitetura e os ADRs de `elo-ai/SPEC.md` e `SPEC.pt-BR.md`.
- Escrever testes antes da implementação quando a tarefa permitir, manter
  commits atômicos e nunca aceitar uma mudança que deixe o projeto quebrado.
- Usar `ELOAI_FAKE_LLM=1` para validações locais que não devem consumir cota.

## Critério de "produto pronto"

O produto só pode ser declarado pronto quando os fluxos de professor e aluno
estiverem funcionais de ponta a ponta, com autenticação, isolamento
multi-tenant/RLS, consentimento LGPD, auditoria de IA, geração estruturada de
planos/slides/exercícios, tutor restrito ao material do tenant, motor
adaptativo explicável, frontend compilável, backend testado e documentação de
setup atualizada.
