package store

import (
	"fmt"
	"strconv"

	"github.com/ralexstokes/relay-monitor/pkg/types"
)

func BuildSlotBoundsFilterClause(query string, slotBounds *types.SlotBounds) string {
	if slotBounds == nil {
		return query
	}
	if slotBounds.StartSlot != nil {
		query = query + ` AND slot >= ` + strconv.FormatUint(*slotBounds.StartSlot, 10)
	}
	if slotBounds.EndSlot != nil {
		query = query + ` AND slot <= ` + strconv.FormatUint(*slotBounds.EndSlot, 10)
	}
	return query
}

func BuildCategoryFilterClause(query string, filter *types.AnalysisQueryFilter) string {
	if filter == nil {
		return query
	}

	return query + ` AND category ` + filter.Comparator + ` '` + fmt.Sprintf("%d", filter.Category) + `'`
}
