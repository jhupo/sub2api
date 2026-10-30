package admin

import (
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/gin-gonic/gin"
)

func (h *UsageHandler) CacheReadAdjustmentDaily(c *gin.Context) {
	zone := c.DefaultQuery("timezone", timezone.Location().String())
	loc, err := time.LoadLocation(zone)
	if err != nil {
		response.BadRequest(c, "Invalid timezone")
		return
	}
	now := time.Now().In(loc)
	start, err := time.ParseInLocation(time.DateOnly, c.DefaultQuery("start_date", now.AddDate(0, 0, -29).Format(time.DateOnly)), loc)
	if err != nil {
		response.BadRequest(c, "Invalid start_date")
		return
	}
	last, err := time.ParseInLocation(time.DateOnly, c.DefaultQuery("end_date", now.Format(time.DateOnly)), loc)
	if err != nil {
		response.BadRequest(c, "Invalid end_date")
		return
	}
	end := last.AddDate(0, 0, 1)
	if !end.After(start) || end.After(start.AddDate(0, 0, 93)) {
		response.BadRequest(c, "Date range must contain 1 to 93 days")
		return
	}
	var accountID int64
	if raw := c.Query("account_id"); raw != "" {
		accountID, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || accountID <= 0 {
			response.BadRequest(c, "Invalid account_id")
			return
		}
	}
	days, err := h.usageService.GetCacheReadAdjustmentDaily(c.Request.Context(), start, end, zone, accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, days)
}
