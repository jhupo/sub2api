package response

import (
	"fmt"
	"math"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestParsePaginationBoundsNumericInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, input := range []string{fmt.Sprint(math.MaxInt), "9999999999999999999999999999999999", "123abc", "-1"} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/?page="+input+"&page_size=1000", nil)
		page, limit := ParsePagination(c)
		require.Equal(t, 1000, limit)
		require.GreaterOrEqual(t, page, 1)
		require.LessOrEqual(t, page, math.MaxInt/limit)
		if input != fmt.Sprint(math.MaxInt) {
			require.Equal(t, 1, page)
		}
	}
}
