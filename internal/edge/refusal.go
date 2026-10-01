package edge

import (
	"fmt"
	"strings"
)

// refusalConsumerLimit is how many applications a refusal names. Three are
// enough to point at the obvious candidate without turning an error message
// into a process list.
const refusalConsumerLimit = 3

// memoryConsumer is one application's resident memory, summed over all of its
// processes. A refusal names it so the operator knows what to close: only an
// executable name and a size, never a path, a command line or a window title.
type memoryConsumer struct {
	Name string
	GiB  float64
}

// capacityRefusal explains a refused model with the numbers behind the
// verdict, in the operator's language. It is the text a client shows when it
// cannot load a model; capacityMessage stays the short English reason the
// control API reports. A refusal whose numbers are unknown falls back to it.
func capacityRefusal(capacity capacityStatus, consumers []memoryConsumer) string {
	switch capacity.Reason {
	case "insufficient_physical_memory":
		if capacity.RequiredPhysicalGiB == nil || capacity.PhysicalHeadroomGiB == nil {
			break
		}
		return fmt.Sprintf("Não há RAM livre para o modelo %s agora: ele precisa de %s GiB (com %s GiB de reserva) e há %s GiB%s. Faltam %s GiB. Feche programas que estejam usando RAM%s ou escolha um modelo menor.",
			capacity.Model, gib(*capacity.RequiredPhysicalGiB), gib(capacity.ReservePhysicalGiB), gib(*capacity.PhysicalHeadroomGiB),
			reclaimClause(capacity.ReclaimablePhysicalGiB), gib(*capacity.RequiredPhysicalGiB-*capacity.PhysicalHeadroomGiB), consumerClause(consumers))
	case "insufficient_commit_headroom":
		if capacity.RequiredCommitGiB == nil || capacity.CommitHeadroomGiB == nil {
			break
		}
		return fmt.Sprintf("Não há memória reservável (commit) para o modelo %s agora: ele precisa de %s GiB (com %s GiB de reserva) e há %s GiB%s. Faltam %s GiB. Feche programas%s ou aumente o arquivo de paginação.",
			capacity.Model, gib(*capacity.RequiredCommitGiB), gib(capacity.ReserveCommitGiB), gib(*capacity.CommitHeadroomGiB),
			reclaimClause(capacity.ReclaimableCommitGiB), gib(*capacity.RequiredCommitGiB-*capacity.CommitHeadroomGiB), consumerClause(consumers))
	case "insufficient_vram_budget":
		if capacity.RequiredVRAMGiB == nil || capacity.DeviceVRAMGiB == nil {
			break
		}
		return fmt.Sprintf("O modelo %s pede %s GiB de VRAM (com %s GiB de reserva) e a GPU tem %s GiB: ele não cabe nesta máquina. Escolha um modelo menor.",
			capacity.Model, gib(*capacity.RequiredVRAMGiB), gib(capacity.ReserveVRAMGiB), gib(*capacity.DeviceVRAMGiB))
	}
	return capacityMessage(capacity.Reason)
}

// needsConsumers reports whether a refusal for this reason names the
// applications holding memory, so the process table is read only then.
func needsConsumers(reason string) bool {
	return reason == "insufficient_physical_memory" || reason == "insufficient_commit_headroom"
}

func reclaimClause(reclaimable *float64) string {
	if reclaimable == nil || *reclaimable <= 0 {
		return ""
	}
	return fmt.Sprintf(", já contando os %s GiB que descarregar o modelo atual libera", gib(*reclaimable))
}

func consumerClause(consumers []memoryConsumer) string {
	if len(consumers) == 0 {
		return ""
	}
	parts := make([]string, 0, len(consumers))
	for _, consumer := range consumers {
		parts = append(parts, fmt.Sprintf("%s %s GiB", consumer.Name, gib(consumer.GiB)))
	}
	list := parts[0]
	if len(parts) > 1 {
		list = strings.Join(parts[:len(parts)-1], ", ") + " e " + parts[len(parts)-1]
	}
	return " (agora os que mais usam são " + list + ")"
}

// gib formats a size with one decimal and a decimal comma, as the operator's
// locale writes it.
func gib(value float64) string {
	return strings.Replace(fmt.Sprintf("%.1f", value), ".", ",", 1)
}
