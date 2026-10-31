package accounteventsctl

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"src.solsynth.dev/sosys/go/pkg/errs"
	"src.solsynth.dev/sosys/stargate/accountevents"
	"src.solsynth.dev/sosys/stargate/model"
)

// ─────────────────────────── own calendar ───────────────────────────

func (d Deps) getCalendar(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	month, year, ok := calendarPeriod(c)
	if !ok {
		return
	}
	viewer := currentViewer(c)
	layer, _ := queryInt(c, "layer")

	calendar, err := d.Service.Calendar(c.Request.Context(), user, accountevents.CalendarOptions{
		Month:              month,
		Year:               year,
		ViewerID:           viewer,
		RegionCode:         regionOf(user),
		IncludeNotableDays: boolQuery(c, "includeNotableDays", false),
		Layer:              layer,
	})
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, calendar)
}

func (d Deps) getMergedCalendar(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	month, year, ok := calendarPeriod(c)
	if !ok {
		return
	}
	layer, _ := queryInt(c, "layer")

	calendar, err := d.Service.MergedCalendar(c.Request.Context(), user, accountevents.CalendarOptions{
		Month:      month,
		Year:       year,
		ViewerID:   currentViewer(c),
		RegionCode: regionOf(user),
		Layer:      layer,
	})
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, calendar)
}

func (d Deps) getCalendarTags(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	accountID, ok := accountIDOf(user)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	tags, err := d.Service.CalendarTags(c.Request.Context(), accountID)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, tags)
}

func (d Deps) getCalendarEvents(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	accountID, ok := accountIDOf(user)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	filterAccount, err := queryUUID(c, "accountId")
	if err != nil {
		validationError(c, "accountId", "The accountId value is not a valid identifier.")
		return
	}
	startTime, _, err := queryInstant(c, "startTime")
	if err != nil {
		validationError(c, "startTime", "The startTime value is not a valid instant.")
		return
	}
	endTime, _, err := queryInstant(c, "endTime")
	if err != nil {
		validationError(c, "endTime", "The endTime value is not a valid instant.")
		return
	}
	offset, take := pagination(c, 50)

	events, total, err := d.Service.AccessibleEvents(c.Request.Context(), accountID, filterAccount,
		startTime, endTime, c.Query("query"), c.QueryArray("tags"), offset, take)
	if err != nil {
		internalError(c, err)
		return
	}
	c.Header("X-Total", strconv.Itoa(total))
	c.JSON(http.StatusOK, events)
}

func (d Deps) createCalendarEvent(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	accountID, ok := accountIDOf(user)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	var request accountevents.CreateCalendarEventRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		validationError(c, "request", "The request body is invalid.")
		return
	}

	event, err := d.Service.CreateEvent(c.Request.Context(), accountID, request)
	if err != nil {
		d.writeCalendarError(c, err)
		return
	}
	c.Header("Location", "/api/accounts/me/calendar/events/"+event.Id)
	c.JSON(http.StatusCreated, event)
}

func (d Deps) getCalendarEvent(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	eventID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, notFound("calendar event"))
		return
	}
	viewer := currentViewer(c)
	event, err := d.Service.Event(c.Request.Context(), eventID, viewer)
	if err != nil {
		c.JSON(http.StatusNotFound, notFound("calendar event"))
		return
	}
	c.JSON(http.StatusOK, event)
}

func (d Deps) updateCalendarEvent(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	accountID, ok := accountIDOf(user)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	eventID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, notFound("calendar event"))
		return
	}
	var request accountevents.UpdateCalendarEventRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		validationError(c, "request", "The request body is invalid.")
		return
	}
	event, err := d.Service.UpdateEvent(c.Request.Context(), accountID, eventID, request)
	if err != nil {
		d.writeCalendarError(c, err)
		return
	}
	c.JSON(http.StatusOK, event)
}

func (d Deps) deleteCalendarEvent(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	accountID, ok := accountIDOf(user)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	eventID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, notFound("calendar event"))
		return
	}
	if err := d.Service.DeleteEvent(c.Request.Context(), accountID, eventID); err != nil {
		d.writeCalendarError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (d Deps) writeCalendarError(c *gin.Context, err error) {
	var validation *accountevents.ValidationError
	switch {
	case errors.As(err, &validation):
		validationError(c, validation.Field, validation.Message)
	case errors.Is(err, accountevents.ErrCalendarEventNotFound):
		c.JSON(http.StatusNotFound, notFound("calendar event"))
	case errors.Is(err, accountevents.ErrNotEventOwner):
		c.JSON(http.StatusForbidden, errs.Forbidden("Calendar event does not belong to user."))
	default:
		internalError(c, err)
	}
}

// ─────────────────────────── notable day lookup ───────────────────────────

func (d Deps) getGeneratedNotableDay(c *gin.Context) {
	if requireCurrentUser(c) == nil {
		return
	}
	key := c.Param("occurrenceKey")
	day, err := d.Notable.GetGeneratedNotableDay(c.Request.Context(), key)
	if err != nil {
		internalError(c, err)
		return
	}
	if day == nil {
		c.JSON(http.StatusNotFound, notFound("notable day"))
		return
	}
	c.JSON(http.StatusOK, day)
}

// ─────────────────────────── search / countdown ───────────────────────────

func (d Deps) searchCalendar(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	accountID, ok := accountIDOf(user)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	filterAccount, err := queryUUID(c, "accountId")
	if err != nil {
		validationError(c, "accountId", "The accountId value is not a valid identifier.")
		return
	}
	startTime, _, err := queryInstant(c, "startTime")
	if err != nil {
		validationError(c, "startTime", "The startTime value is not a valid instant.")
		return
	}
	endTime, _, err := queryInstant(c, "endTime")
	if err != nil {
		validationError(c, "endTime", "The endTime value is not a valid instant.")
		return
	}
	notableTag, ok := parseTag(c.Query("notableDayTag"))
	if !ok {
		validationError(c, "notableDayTag", "Invalid notable day tag.")
		return
	}
	region := strings.TrimSpace(c.Query("region"))
	if region == "" {
		region = regionOf(user)
	}
	offset, take := pagination(c, 50)

	items, total, err := d.Service.Search(c.Request.Context(), accountID, region,
		c.Query("query"), filterAccount, startTime, endTime, c.QueryArray("tags"), notableTag, offset, take)
	if err != nil {
		internalError(c, err)
		return
	}
	c.Header("X-Total", strconv.Itoa(total))
	c.JSON(http.StatusOK, items)
}

func (d Deps) getCountdown(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	offset, take := pagination(c, 5)
	tag, _ := parseTag(c.Query("tag"))
	layer, _ := queryInt(c, "layer")

	items, total, err := d.Service.Countdown(c.Request.Context(), user, currentViewer(c), regionOf(user),
		boolQuery(c, "includeNotableDays", true), tag, take, offset, layer)
	if err != nil {
		internalError(c, err)
		return
	}
	c.Header("X-Total", strconv.Itoa(total))
	c.JSON(http.StatusOK, items)
}

// ─────────────────────────── subscriptions ───────────────────────────

func (d Deps) getSubscriptions(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	accountID, ok := accountIDOf(user)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	ids, err := d.Service.Subscriptions(c.Request.Context(), accountID)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, ids)
}

func (d Deps) subscribe(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	accountID, ok := accountIDOf(user)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	targetID, err := uuid.Parse(c.Param("accountId"))
	if err != nil {
		validationError(c, "accountId", "The accountId value is not a valid identifier.")
		return
	}
	subscription, err := d.Service.Subscribe(c.Request.Context(), accountID, targetID)
	if err != nil {
		var validation *accountevents.ValidationError
		if errors.As(err, &validation) {
			validationError(c, validation.Field, validation.Message)
			return
		}
		internalError(c, err)
		return
	}
	c.Header("Location", "/api/accounts/me/calendar/subscriptions/"+targetID.String())
	c.JSON(http.StatusCreated, subscription)
}

func (d Deps) unsubscribe(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	accountID, ok := accountIDOf(user)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	targetID, err := uuid.Parse(c.Param("accountId"))
	if err != nil {
		validationError(c, "accountId", "The accountId value is not a valid identifier.")
		return
	}
	if err := d.Service.Unsubscribe(c.Request.Context(), accountID, targetID); err != nil {
		if errors.Is(err, accountevents.ErrSubscriptionNotFound) {
			c.JSON(http.StatusNotFound, notFound("subscription"))
			return
		}
		internalError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (d Deps) getSubscribers(c *gin.Context) {
	user := requireCurrentUser(c)
	if user == nil {
		return
	}
	accountID, ok := accountIDOf(user)
	if !ok {
		c.JSON(http.StatusUnauthorized, errs.Unauthorized(""))
		return
	}
	ids, err := d.Service.Subscribers(c.Request.Context(), accountID)
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, ids)
}

// ─────────────────────────── public account calendar ───────────────────────────

func (d Deps) lookupAccount(c *gin.Context) *model.Account {
	name := c.Param("name")
	account, err := d.Accounts.GetAccountByName(c.Request.Context(), name)
	if err != nil {
		c.JSON(http.StatusNotFound, notFound(name))
		return nil
	}
	return account
}

func (d Deps) getAccountCalendar(c *gin.Context) {
	account := d.lookupAccount(c)
	if account == nil {
		return
	}
	month, year, ok := calendarPeriod(c)
	if !ok {
		return
	}
	layer, _ := queryInt(c, "layer")

	calendar, err := d.Service.Calendar(c.Request.Context(), account, accountevents.CalendarOptions{
		Month:              month,
		Year:               year,
		ViewerID:           currentViewer(c),
		RegionCode:         regionOf(account),
		IncludeNotableDays: boolQuery(c, "includeNotableDays", false),
		Layer:              layer,
		ReplaceInvisible:   true,
	})
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, calendar)
}

func (d Deps) getAccountMergedCalendar(c *gin.Context) {
	account := d.lookupAccount(c)
	if account == nil {
		return
	}
	month, year, ok := calendarPeriod(c)
	if !ok {
		return
	}
	layer, _ := queryInt(c, "layer")

	calendar, err := d.Service.MergedCalendar(c.Request.Context(), account, accountevents.CalendarOptions{
		Month:            month,
		Year:             year,
		ViewerID:         currentViewer(c),
		RegionCode:       regionOf(account),
		Layer:            layer,
		ReplaceInvisible: true,
	})
	if err != nil {
		internalError(c, err)
		return
	}
	c.JSON(http.StatusOK, calendar)
}

func (d Deps) getAccountCountdown(c *gin.Context) {
	account := d.lookupAccount(c)
	if account == nil {
		return
	}
	offset, take := pagination(c, 5)
	tag, _ := parseTag(c.Query("tag"))
	layer, _ := queryInt(c, "layer")
	items, total, err := d.Service.Countdown(c.Request.Context(), account, currentViewer(c), regionOf(account),
		boolQuery(c, "includeNotableDays", true), tag, take, offset, layer)
	if err != nil {
		internalError(c, err)
		return
	}
	c.Header("X-Total", strconv.Itoa(total))
	c.JSON(http.StatusOK, items)
}

func (d Deps) getTimeline(c *gin.Context) {
	account := d.lookupAccount(c)
	if account == nil {
		return
	}
	accountID, ok := accountIDOf(account)
	if !ok {
		c.JSON(http.StatusNotFound, notFound(c.Param("name")))
		return
	}
	offset, take := pagination(c, 20)
	items, total, err := d.Service.Timeline(c.Request.Context(), accountID, offset, take)
	if err != nil {
		internalError(c, err)
		return
	}
	c.Header("X-Total", strconv.Itoa(total))
	c.JSON(http.StatusOK, items)
}

func (d Deps) getAccountCalendarEvents(c *gin.Context) {
	account := d.lookupAccount(c)
	if account == nil {
		return
	}
	accountID, ok := accountIDOf(account)
	if !ok {
		c.JSON(http.StatusNotFound, notFound(c.Param("name")))
		return
	}
	startTime, _, err := queryInstant(c, "startTime")
	if err != nil {
		validationError(c, "startTime", "The startTime value is not a valid instant.")
		return
	}
	endTime, _, err := queryInstant(c, "endTime")
	if err != nil {
		validationError(c, "endTime", "The endTime value is not a valid instant.")
		return
	}
	offset, take := pagination(c, 50)

	events, total, err := d.Service.AccountEvents(c.Request.Context(), accountID, currentViewer(c),
		startTime, endTime, "", nil, offset, take)
	if err != nil {
		internalError(c, err)
		return
	}
	for i := range events {
		events[i].Account = account
	}
	c.Header("X-Total", strconv.Itoa(total))
	c.JSON(http.StatusOK, events)
}

func (d Deps) getAccountCalendarEvent(c *gin.Context) {
	account := d.lookupAccount(c)
	if account == nil {
		return
	}
	accountID, ok := accountIDOf(account)
	if !ok {
		c.JSON(http.StatusNotFound, notFound(c.Param("name")))
		return
	}
	eventID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, notFound("calendar event"))
		return
	}
	event, err := d.Service.Event(c.Request.Context(), eventID, currentViewer(c))
	if err != nil || event.AccountId != accountID.String() {
		c.JSON(http.StatusNotFound, notFound("calendar event"))
		return
	}
	event.Account = account
	c.JSON(http.StatusOK, event)
}

func (d Deps) getCalendarEventByID(c *gin.Context) {
	eventID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, notFound("calendar event"))
		return
	}
	event, err := d.Service.Event(c.Request.Context(), eventID, currentViewer(c))
	if err != nil {
		c.JSON(http.StatusNotFound, notFound("calendar event"))
		return
	}
	c.JSON(http.StatusOK, event)
}
