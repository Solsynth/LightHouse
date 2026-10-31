package accounteventsctl

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/lighthouse/shared/pkg/errs"
	"src.solsynth.dev/sosys/stargate/accountevents"
)

func notableDayNotFound() *errs.ApiError {
	return errs.New("PASSPORT_NOTABLE_DAY_NOT_FOUND", "Notable day not found.", http.StatusNotFound)
}

func (d Deps) listNotableDays(c *gin.Context) {
	year := time.Now().UTC().Year()
	if parsed, err := queryInt(c, "year"); err == nil && parsed != nil {
		year = *parsed
	}
	region := strings.TrimSpace(c.Query("region"))
	if region == "" {
		region = "CN"
	}
	tag, _ := parseTag(c.Query("tag"))
	layer, _ := queryInt(c, "layer")
	offset, take := pagination(c, 50)

	days, total, err := d.Notable.ListStoredNotableDays(c.Request.Context(), year, region, tag, layer, offset, take)
	if err != nil {
		internalError(c, err)
		return
	}
	c.Header("X-Total", strconv.Itoa(total))
	c.JSON(http.StatusOK, days)
}

func (d Deps) getNotableDay(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, notableDayNotFound())
		return
	}
	entity, err := d.Store.GetNotableDayEntity(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, notableDayNotFound())
		return
	}
	c.JSON(http.StatusOK, accountevents.StoredNotableDayFromEntity(entity))
}

func (d Deps) createNotableDay(c *gin.Context) {
	var request accountevents.NotableDayRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		validationError(c, "request", "The request body is invalid.")
		return
	}
	if !request.EndDate.Time().After(request.StartDate.Time()) && !request.IsPeriod {
		c.JSON(http.StatusBadRequest, errs.New("PASSPORT_NOTABLE_DAY_INVALID_DATE",
			"End date must be after start date.", http.StatusBadRequest))
		return
	}
	day := notableDayFromRequest(request)
	if err := d.Store.CreateNotableDay(c.Request.Context(), day); err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, day)
}

func (d Deps) updateNotableDay(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, notableDayNotFound())
		return
	}
	if _, err := d.Store.GetNotableDayEntity(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusNotFound, notableDayNotFound())
		return
	}
	var request accountevents.NotableDayRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		validationError(c, "request", "The request body is invalid.")
		return
	}
	day := notableDayFromRequest(request)
	day.Id = id.String()
	if err := d.Store.UpdateNotableDay(c.Request.Context(), day); err != nil {
		if errors.Is(err, accountevents.ErrNotFound) {
			c.JSON(http.StatusNotFound, notableDayNotFound())
			return
		}
		internalError(c, err)
		return
	}
	if entity, err := d.Store.GetNotableDayEntity(c.Request.Context(), id); err == nil {
		c.JSON(http.StatusOK, accountevents.StoredNotableDayFromEntity(entity))
		return
	}
	c.JSON(http.StatusOK, day)
}

func (d Deps) deleteNotableDay(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, notableDayNotFound())
		return
	}
	if err := d.Store.DeleteNotableDay(c.Request.Context(), id); err != nil {
		if errors.Is(err, accountevents.ErrNotFound) {
			c.JSON(http.StatusNotFound, notableDayNotFound())
			return
		}
		internalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func notableDayFromRequest(request accountevents.NotableDayRequest) *accountevents.StoredNotableDay {
	region := strings.TrimSpace(request.Region)
	if region == "" {
		region = "CN"
	}
	tags := request.Tags
	if tags == nil {
		tags = []accountevents.NotableDayTag{}
	}
	holidayDays := request.HolidayDays
	if holidayDays == nil {
		holidayDays = []string{}
	}
	isAllDay := true
	if request.IsAllDay != nil {
		isAllDay = *request.IsAllDay
	}
	return &accountevents.StoredNotableDay{
		Name:           request.Name,
		Description:    request.Description,
		LocalName:      request.LocalName,
		LocalizableKey: request.LocalizableKey,
		StartDate:      request.StartDate,
		EndDate:        request.EndDate,

		Tags:              tags,
		Meta:              request.Meta,
		IsAllDay:          isAllDay,
		RecurrencePattern: request.RecurrencePattern,
		IsPeriod:          request.IsPeriod,
		HolidayDays:       holidayDays,
		DisplayOrder:      request.DisplayOrder,
	}
}
