package orchestrator

import "fmt"

// poDecidePrompt builds the prompt for the PO agent's "decide what to work
// on next" call. The dev_prompt it asks for must be self-contained: the Dev
// agent that receives it has no memory of anything except what is written
// in that prompt.
func poDecidePrompt(vision, state, backlogJSON string) string {
	return fmt.Sprintf(`Você é o Product Owner de um projeto de software. Sua tarefa é decidir
qual é o PRÓXIMO passo de trabalho, com base na visão do produto, no estado
atual e no backlog abaixo.

## Visão do produto
%s

## Estado atual
%s

## Backlog (JSON)
%s

## Sua tarefa

Decida uma de duas coisas:
1. Existe trabalho a fazer: escolha a próxima task do backlog (ou refine uma
   existente) e escreva um dev_prompt completo para o Desenvolvedor executá-la.
2. O produto está pronto segundo o critério de "produto pronto" da visão:
   sinalize product_done.

O dev_prompt que você escrever é a ÚNICA informação que o Desenvolvedor vai
receber — ele não tem memória de nada além do que estiver nesse prompt. Portanto
o dev_prompt precisa ser AUTOCONTIDO e DEVE incluir, sempre que relevante:
- A descrição completa da task (o que fazer, critérios de aceite).
- As convenções arquiteturais obrigatórias do projeto: arquitetura hexagonal
  (domain/ports/adapters), TDD com ciclo RED antes de GREEN (escrever o teste
  que falha antes de qualquer código de implementação), e commits atômicos por
  peça lógica.
- Qualquer contexto de arquivos/módulos relevantes que você souber pelo estado
  atual.

## Formato de resposta

Responda APENAS com um objeto JSON no formato exato abaixo. Não inclua
markdown, cercas de código, nem nenhum texto antes ou depois do JSON:

{"action": "next_task" | "product_done", "task_id": "...", "dev_prompt": "...", "reasoning": "..."}
`, vision, state, backlogJSON)
}

// poEvaluatePrompt builds the prompt for the PO agent's "evaluate the Dev's
// report" call. It explicitly demands rigor: the PO must not simply accept
// the Dev's own claim of success.
func poEvaluatePrompt(vision, state, reportJSON string) string {
	return fmt.Sprintf(`Você é o Product Owner de um projeto de software. O Desenvolvedor acabou
de reportar o resultado de uma task. Avalie esse relatório com RIGOR.

## Visão do produto
%s

## Estado atual
%s

## Relatório do Desenvolvedor (JSON)
%s

## Sua tarefa

Seja rigoroso: não aceite a alegação do relatório de bandeja. Procure indícios
REAIS de sucesso — testes de fato executados e passando, arquivos de fato
alterados no repositório, evidência concreta no raw_output — e não apenas a
afirmação de que "deu tudo certo". Se o relatório não trouxer evidência
suficiente, trate como reject ou block, mesmo que o texto pareça confiante.

Decida um veredito:
- accept: a task foi completada corretamente e com evidência real.
- reject: a task não foi completada como esperado, mas vale tentar de novo;
  escreva um correction_prompt AUTOCONTIDO (mesmas regras do dev_prompt:
  arquitetura hexagonal, TDD RED antes de GREEN, commits atômicos) explicando
  o que corrigir.
- block: a task está travada e não deve ser tentada de novo sem intervenção
  humana.

Se o estado do projeto mudou de forma que valha registrar, preencha
state_update com a versão COMPLETA e nova de state.md (ela substitui o
arquivo inteiro, não é um adendo).

## Formato de resposta

Responda APENAS com um objeto JSON no formato exato abaixo. Não inclua
markdown, cercas de código, nem nenhum texto antes ou depois do JSON:

{"verdict": "accept" | "reject" | "block", "reasoning": "...", "correction_prompt": "...", "state_update": "..."}
`, vision, state, reportJSON)
}
