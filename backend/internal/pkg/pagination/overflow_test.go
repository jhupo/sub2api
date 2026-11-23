package pagination

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOffsetSaturatesWithoutOverflow(t *testing.T) {
	for _, limit := range []int{1, 2, 20, 1000} {
		for _, page := range []int{-1, 0, 1, math.MaxInt / limit, math.MaxInt} {
			params := PaginationParams{Page: page, PageSize: limit}
			offset := params.Offset()
			require.GreaterOrEqual(t, offset, 0)
			require.LessOrEqual(t, offset, math.MaxInt-limit)
		}
	}
}
