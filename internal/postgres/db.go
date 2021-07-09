package postgres

import (
	"database/sql"
	"flag"
	"fmt"
	"github.com/Encinarus/genconplanner/internal/events"
	"github.com/lib/pq"
	"log"
	"sort"
	"strings"
	"time"
)

var dbConnectString = flag.String("db", "", "postgres connect string")

var INDIANAPOLIS, _ = time.LoadLocation("America/Indiana/Indianapolis")

func OpenDb() (*sql.DB, error) {
	fmt.Println("dbString", *dbConnectString)
	return sql.Open("postgres", *dbConnectString)
}

type GameFamily struct {
	Name  string
	BggId int64
}

type Game struct {
	Name       string
	BggId      int64
	FamilyIds  []int64
	LastUpdate time.Time
	NumRatings int64
	AvgRatings float64
}

func (g *Game) Upsert(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}

	// Cleanup transaction!
	defer func() {
		var txErr error
		if err != nil {
			txErr = tx.Rollback()
		} else {
			txErr = tx.Commit()
		}
		if txErr != nil {
			log.Printf("Error while resolving transaction: %v", txErr)
		}
	}()

	_, err = tx.Exec(`
INSERT INTO boardgame(name, bgg_id, family_ids, num_ratings, avg_ratings, last_update)
VALUES ($1, $2, $3, $4, $5, CURRENT_DATE)
ON CONFLICT (bgg_id) DO UPDATE SET name = $1, family_ids = $3, num_ratings = $4, avg_ratings = $5, last_update = CURRENT_DATE
`, g.Name, g.BggId, pq.Array(g.FamilyIds), g.NumRatings, g.AvgRatings)

	if err != nil {
		return err
	}

	return err
}

func LoadGames(db *sql.DB) ([]*Game, error) {
	rows, err := db.Query(`
SELECT 
    name,
	bgg_id, 
    family_ids,
   num_ratings,
   avg_ratings,
    last_update
FROM boardgame bg
`)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	games := make([]*Game, 0)

	for rows.Next() {
		var g Game
		var timeHolder pq.NullTime
		var numRatingHolder sql.NullInt64
		var avgRatingHolder sql.NullFloat64
		err = rows.Scan(&g.Name, &g.BggId, pq.Array(&g.FamilyIds), &numRatingHolder, &avgRatingHolder, &timeHolder)
		if err != nil {
			return nil, err
		}
		if timeHolder.Valid {
			g.LastUpdate = timeHolder.Time
		}
		// We don't check for valid since they'll default to 0 anyway.
		g.NumRatings = numRatingHolder.Int64
		g.AvgRatings = avgRatingHolder.Float64
		games = append(games, &g)
	}
	return games, nil
}

type User struct {
	Email       string
	DisplayName string
	CalendarId  string
}

type CalendarEventCluster struct {
	Title            string
	StartTime        time.Time
	EndTime          time.Time
	GenconUrl        string
	PlannerUrl       string
	ShortCategory    string
	ShortDescription string
	SimilarCount     int
}

func newClusterForEvent(event *events.GenconEvent) *CalendarEventCluster {
	return &CalendarEventCluster{
		Title:            event.Title,
		StartTime:        event.StartTime,
		EndTime:          event.EndTime,
		GenconUrl:        event.GenconLink(),
		PlannerUrl:       event.PlannerLink(),
		ShortCategory:    event.ShortCategory,
		ShortDescription: event.ShortDescription,
		SimilarCount:     1,
	}
}

func LoadStarredEventClusters(db *sql.DB, userEmail string, year int, starredEvents []*events.GenconEvent) ([]*CalendarEventCluster, error) {
	rows, err := db.Query(`
SELECT 
    CASE e.day_of_week 
		WHEN 3 THEN 'wed'
		WHEN 4 THEN 'thu'
		WHEN 5 THEN 'fri'
		WHEN 6 THEN 'sat'
		WHEN 0 THEN 'sun'
	END AS day_of_week,
    ARRAY_AGG(se.event_id) event_ids
FROM starred_events se 
     JOIN events e ON e.event_id = se.event_id
WHERE se.email = $1
  AND e.year = $2
  AND e.active
GROUP BY e.cluster_key, day_of_week
`, userEmail, year)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	eventsById := make(map[string]*events.GenconEvent)
	for _, e := range starredEvents {
		eventsById[e.EventId] = e
	}

	groupedEvents := make([]*CalendarEventCluster, 0)
	for rows.Next() {
		eventIds := make([]string, 0)
		var dayOfWeek string
		err = rows.Scan(&dayOfWeek, pq.Array(&eventIds))
		if err != nil {
			return nil, err
		}

		dayGroupEvents := make([]*events.GenconEvent, 0, len(eventIds))
		for _, id := range eventIds {
			// Guard against events being starred between the load and
			// here. Should be _super_ rare, handle anyway.
			if e, present := eventsById[id]; present {
				dayGroupEvents = append(dayGroupEvents, e)
			} else {
				log.Printf("Can't find event %v in events", id)
			}
		}
		// We sort the events by start time so we can reference
		// the earliest one in each cluster
		sort.Slice(dayGroupEvents, func(i, j int) bool {
			return dayGroupEvents[i].StartTime.Before(dayGroupEvents[j].StartTime)
		})

		cluster := newClusterForEvent(dayGroupEvents[0])

		for _, event := range dayGroupEvents[1:] {
			if event.StartTime.After(cluster.EndTime) {
				groupedEvents = append(groupedEvents, cluster)
				cluster = newClusterForEvent(event)
			} else if event.EndTime.After(cluster.EndTime) {
				cluster.EndTime = event.EndTime
				cluster.SimilarCount++
			}
		}

		groupedEvents = append(groupedEvents, cluster)
	}

	log.Printf("Returning %v groups", len(groupedEvents))
	return groupedEvents, nil
}

func LoadStarredEvents(db *sql.DB, userEmail string, year int) ([]*events.GenconEvent, error) {
	fields := "e1." + strings.Join(eventFields(), ", e1.")
	rows, err := db.Query(fmt.Sprintf(`
SELECT %s, true
FROM events e1 
     JOIN starred_events se ON se.event_id = e1.event_id
WHERE se.email = $1
  AND e1.year = $2
  AND e1.active
ORDER BY e1.start_time`, fields), userEmail, year)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	loadedEvents := make([]*events.GenconEvent, 0)
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		loadedEvents = append(loadedEvents, event)
	}
	return loadedEvents, nil
}

type UserStarredEvents struct {
	Email         string
	StarredEvents []string
}

func UpdateStarredEvent(db *sql.DB, email string, eventId string, related bool, add bool) (*UserStarredEvents, error) {
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}

	if related {
		// Delete all similar events first, regardless
		_, err = tx.Exec(`
DELETE FROM starred_events s
WHERE s.email = $1
  AND s.event_id in (
	  SELECT e2.event_id
	  FROM events e1 join events e2 on e1.year = e2.year
          AND e1.short_category = e2.short_category
	      AND e1.title = e2.title
          AND e1.cluster_key = e2.cluster_key
	  WHERE e1.event_id = $2
  )
`, email, eventId)

		if err == nil && add {
			// insert via select related ids
			_, err = tx.Exec(`
INSERT INTO starred_events(email, event_id)
SELECT $1, e2.event_id
FROM events e1 join events e2 on e1.year = e2.year
    AND e1.short_category = e2.short_category
    AND e1.title = e2.title   
    AND e1.cluster_key = e2.cluster_key
WHERE e1.event_id = $2
ON CONFLICT DO NOTHING
`, email, eventId)
		}
	} else if add {
		// insert one record
		_, err = tx.Exec(`
INSERT INTO starred_events(email, event_id)
VALUES ($1, $2)
ON CONFLICT DO NOTHING
`, email, eventId)
	} else {
		// delete record
		_, err = tx.Exec(`
DELETE FROM starred_events s
WHERE s.email = $1
  AND s.event_id = $2
`, email, eventId)
	}

	if err != nil {
		tx.Rollback()
		return nil, err
	} else {
		starredEvents := UserStarredEvents{
			Email: email,
		}

		err = tx.QueryRow(`
SELECT ARRAY(SELECT event_id
FROM starred_events
WHERE email = $1);
`, email).Scan(pq.Array(&starredEvents.StarredEvents))
		if err != nil {
			tx.Rollback()
			return nil, err
		} else {
			tx.Commit()
			return &starredEvents, nil
		}
	}
}

func GetStarredIds(db *sql.DB, email string) (*UserStarredEvents, error) {
	starredEvents := UserStarredEvents{
		Email: email,
	}

	err := db.QueryRow(`
SELECT ARRAY(SELECT event_id
FROM starred_events
WHERE email = $1);
`, email).Scan(pq.Array(&starredEvents.StarredEvents))
	if err != nil {
		return nil, err
	} else {
		return &starredEvents, nil
	}
}

func LoadOrCreateUser(db *sql.DB, email string) (*User, error) {
	rows, err := db.Query(`
SELECT 
       email, 
       display_name
FROM users
WHERE email=$1
`, email)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var user *User
	for rows.Next() {
		var loadedUser User
		if err := rows.Scan(
			&loadedUser.Email,
			&loadedUser.DisplayName,
		); err != nil {
			log.Fatalf("Error loading user %v", err)
		} else {
			user = &loadedUser
		}

		break
	}

	if user == nil {
		// Time to create a user
		user = &User{
			Email:       email,
			DisplayName: strings.Split(email, "@")[0],
		}

		_, err := db.Exec("INSERT INTO users(email, display_name) VALUES ($1, $2)",
			user.Email, user.DisplayName)
		if err != nil {
			log.Fatalf("Error creating user, %v", user)
		}
	}

	return user, nil
}

type CategorySummary struct {
	Name  string
	Code  string
	Count int
}

type EventGroup struct {
	Name          string
	EventId       string
	Description   string
	ShortCategory string
	GameSystem    string
	Count         int
	WedTickets    int
	ThursTickets  int
	FriTickets    int
	SatTickets    int
	SunTickets    int
	TotalTickets  int
}

func rowToGroup(rows *sql.Rows) (*EventGroup, error) {
	var group EventGroup
	if err := rows.Scan(
		&group.EventId,
		&group.Name,
		&group.Description,
		&group.ShortCategory,
		&group.GameSystem,
		// Aggregate fields
		&group.Count,
		&group.TotalTickets,
		&group.WedTickets,
		&group.ThursTickets,
		&group.FriTickets,
		&group.SatTickets,
		&group.SunTickets,
	); err != nil {
		return nil, err
	}
	return &group, nil
}

func LoadEventGroups(db *sql.DB, cat string, year int, days []int) ([]*EventGroup, error) {
	daysOfWeek := []int{3, 4, 5, 6, 0}

	if days != nil && len(days) > 0 {
		daysOfWeek = days
	}
	rows, err := db.Query(`
SELECT 
       e.event_id,
	   e.title,
	   e.short_description,
	   e.short_category,
       e.game_system,
	   c.num_events,
	   c.tickets_available,
	   c.wednesday_tickets,
	   c.thursday_tickets,
	   c.friday_tickets,
	   c.saturday_tickets,
	   c.sunday_tickets
FROM events e 
	JOIN (
		SELECT 
			   cluster_key,
			   short_category,
			   title,
			   min(start_time) as start_time,
			   count(1) as num_events,
			   sum(tickets_available) as tickets_available,
			   sum(CASE WHEN day_of_week = 3 THEN tickets_available ELSE 0 END) as wednesday_tickets,
			   sum(CASE WHEN day_of_week = 4 THEN tickets_available ELSE 0 END) as thursday_tickets,
			   sum(CASE WHEN day_of_week = 5 THEN tickets_available ELSE 0 END) as friday_tickets,
			   sum(CASE WHEN day_of_week = 6 THEN tickets_available ELSE 0 END) as saturday_tickets,
			   sum(CASE WHEN day_of_week = 0 THEN tickets_available ELSE 0 END) as sunday_tickets	   
		FROM events
		WHERE active and year=$1 and short_category=$2
		GROUP BY cluster_key, short_category, title
		) as c ON e.title = c.title 
		       AND e.short_category = c.short_category
			   AND e.cluster_key = c.cluster_key
			   AND e.start_time = c.start_time
			   AND e.day_of_week = ANY ($3)
WHERE e.year = $1
ORDER BY c.tickets_available > 0 desc, title`, year, cat, pq.Array(daysOfWeek))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	groups := make([]*EventGroup, 0)

	for rows.Next() {
		group, err := rowToGroup(rows)
		if err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}

	return groups, nil
}

func LoadCategorySummary(db *sql.DB, year int) ([]*CategorySummary, error) {
	rows, err := db.Query(`
SELECT event_type, COUNT(1)
FROM events
where active and year = $1
GROUP BY event_type
ORDER BY event_type ASC`, year)

	if err != nil {
		return nil, err
	}
	defer rows.Close()
	countsPerCategory := make([]*CategorySummary, 0)
	for rows.Next() {
		var summary CategorySummary

		if err = rows.Scan(&summary.Name, &summary.Count); err != nil {
			return nil, err
		}
		summary.Code = strings.Split(summary.Name, " ")[0]
		countsPerCategory = append(countsPerCategory, &summary)
	}
	return countsPerCategory, nil
}

func LoadSimilarEvents(db *sql.DB, eventId string, userEmail string) ([]*events.GenconEvent, error) {
	// Might be slight overkill ensuring that the year matches, but
	// folks could submit the same event two years in a row with the same
	// description, making it cluster the same.
	year := events.YearFromEvent(eventId)

	fields := "e1." + strings.Join(eventFields(), ", e1.")
	rows, err := db.Query(fmt.Sprintf(`
SELECT %s, se.event_id is not null
FROM events e1 
     JOIN events e2 on e1.year = e2.year
          AND e1.short_category = e2.short_category
          AND e1.title = e2.title
          AND e1.cluster_key = e2.cluster_key
     LEFT JOIN starred_events se ON se.event_id = e1.event_id AND se.email = $3
WHERE e2.event_id = $1
  AND e1.year = $2
ORDER BY e1.start_time`, fields), eventId, year, userEmail)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	loadedEvents := make([]*events.GenconEvent, 0)
	for rows.Next() {
		event, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		loadedEvents = append(loadedEvents, events.NormalizeEvent(event))
	}
	return loadedEvents, nil
}

type ParsedQuery struct {
	// TODO(alek): make a significantly more robust query parser
	// add exact match on fields,
	TextQueries     []string
	Year            int
	DaysOfWeek      map[string]bool
	RawQuery        string
	StartBeforeHour int
	StartAfterHour  int
	EndBeforeHour   int
	EndAfterHour    int
}

func FindEvents(db *sql.DB, query *ParsedQuery) ([]*EventGroup, error) {
	innerFrom := "events"
	innerWhere := fmt.Sprintf("active AND year = %v", query.Year)
	innerWhere = fmt.Sprintf("%v AND EXTRACT(HOUR FROM start_time AT TIME ZONE 'EDT') <= %v", innerWhere, query.StartBeforeHour)
	innerWhere = fmt.Sprintf("%v AND EXTRACT(HOUR FROM start_time AT TIME ZONE 'EDT') >= %v", innerWhere, query.StartAfterHour)
	innerWhere = fmt.Sprintf("%v AND EXTRACT(HOUR FROM end_time AT TIME ZONE 'EDT') <= %v", innerWhere, query.EndBeforeHour)
	innerWhere = fmt.Sprintf("%v AND EXTRACT(HOUR FROM end_time AT TIME ZONE 'EDT') >= %v", innerWhere, query.EndAfterHour)
	titleRank := "1"
	searchRank := "1"

	tsquery := strings.Join(query.TextQueries, " & ")
	if len(tsquery) > 0 {
		innerFrom = fmt.Sprintf("%v, to_tsquery('%v') q", innerFrom, tsquery)
		innerWhere = fmt.Sprintf("%v AND search_key @@ q", innerWhere)
		titleRank = "min(ts_rank(title_tsv, q))"
		searchRank = "min(ts_rank(search_key, q))"
	}

	innerQuery := fmt.Sprintf(`
SELECT 
	cluster_key,
	short_category,
	title,
	min(start_time) as start_time,
	count(1) as num_events,
	sum(tickets_available) as tickets_available,
	sum(CASE WHEN day_of_week = 3 THEN tickets_available ELSE 0 END) as wed_tickets,
	sum(CASE WHEN day_of_week = 4 THEN tickets_available ELSE 0 END) as thu_tickets,
	sum(CASE WHEN day_of_week = 5 THEN tickets_available ELSE 0 END) as fri_tickets,
	sum(CASE WHEN day_of_week = 6 THEN tickets_available ELSE 0 END) as sat_tickets,
	sum(CASE WHEN day_of_week = 0 THEN tickets_available ELSE 0 END) as sun_tickets,
    %v as title_rank,
    %v as search_rank
FROM %v
WHERE %v
GROUP BY cluster_key, short_category, title
`, titleRank, searchRank, innerFrom, innerWhere)

	// Default to true so we don't filter anything out
	// if no days were requested
	dayPart := "true"
	if len(query.DaysOfWeek) > 0 {
		var days []string
		for d := range query.DaysOfWeek {
			if query.DaysOfWeek[d] {
				days = append(days, fmt.Sprintf("c.%v_tickets > 0", d))
			}
		}
		dayPart = strings.Join(days, " OR ")
	}
	fullWhere := fmt.Sprintf("e.year = %v AND (%v)", query.Year, dayPart)

	fullQuery := fmt.Sprintf(`
SELECT 
       e.event_id,
	   e.title,
	   e.short_description,
	   e.short_category,
       e.game_system,       
	   c.num_events,
	   c.tickets_available,
	   c.wed_tickets,
	   c.thu_tickets,
	   c.fri_tickets,
	   c.sat_tickets,
	   c.sun_tickets
FROM events e JOIN (%v) AS c 
	ON e.title = c.title
    AND e.short_category = c.short_category
    AND e.cluster_key = c.cluster_key
    AND e.start_time = c.start_time
WHERE %v
ORDER BY c.title_rank desc, c.search_rank desc, c.tickets_available desc
`, innerQuery, fullWhere)

	loadedEvents := make([]*EventGroup, 0)
	rows, err := db.Query(fullQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Load all the events
	for rows.Next() {
		group, err := rowToGroup(rows)
		if err != nil {
			return nil, err
		}

		loadedEvents = append(loadedEvents, group)
	}
	return loadedEvents, nil
}

func loadEventIds(tx *sql.Tx, year int) (map[string]time.Time, map[string]time.Time, error) {
	// load all events: ids + last update time
	rows, err := tx.Query(`
SELECT event_id, active, last_modified
FROM events
WHERE year=$1`, year)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var activeEvents map[string]time.Time
	var inactiveEvents map[string]time.Time
	activeEvents = make(map[string]time.Time)
	inactiveEvents = make(map[string]time.Time)

	for rows.Next() {
		var id string
		var active bool
		var updateTime time.Time

		err := rows.Scan(&id, &active, &updateTime)
		if err != nil {
			return nil, nil, err
		}

		if active {
			activeEvents[id] = updateTime
		} else {
			inactiveEvents[id] = updateTime
		}
	}

	return activeEvents, inactiveEvents, nil
}

func bulkDelete(tx *sql.Tx, deletedEvents []string) error {
	// Deletes aren't true deletes, we mark them as inactive
	batchSize := 100
	for len(deletedEvents) > 0 {
		if len(deletedEvents) < batchSize {
			batchSize = len(deletedEvents)
		}
		batch := make([]string, 0, batchSize)
		for _, eventId := range deletedEvents[0:batchSize:batchSize] {
			batch = append(batch, "'"+eventId+"'")
		}

		deletedEvents = deletedEvents[batchSize:]
		updateStatement := fmt.Sprintf(
			"UPDATE events SET active = FALSE WHERE event_id in (%s)",
			strings.Join(batch, ","))

		_, err := tx.Exec(updateStatement)

		if err != nil {
			log.Printf("Error on processing event: %s %v", batch, err.(pq.PGError))
			return err
		}
	}
	return nil
}

func BulkUpdateEvents(tx *sql.Tx, parsedEvents []*events.GenconEvent) error {
	year := parsedEvents[0].Year
	activeEvents, inactiveEvents, err := loadEventIds(tx, year)
	persistedEvents := make(map[string]time.Time, len(activeEvents)+len(inactiveEvents))
	for id, updateTime := range activeEvents {
		persistedEvents[id] = updateTime
	}
	for id, updateTime := range inactiveEvents {
		persistedEvents[id] = updateTime
	}

	if err != nil {
		return err
	}
	log.Printf("Loaded %d Rows\n", len(persistedEvents))

	var newEvents []*events.GenconEvent
	var updatedEvents []*events.GenconEvent

	latestUpdate := parsedEvents[0].LastModified
	for _, parsedEvent := range parsedEvents {
		if _, found := persistedEvents[parsedEvent.EventId]; found {
			updatedEvents = append(updatedEvents, parsedEvent)
			delete(activeEvents, parsedEvent.EventId)
		} else {
			newEvents = append(newEvents, parsedEvent)
		}
		if latestUpdate.Before(parsedEvent.LastModified) {
			latestUpdate = parsedEvent.LastModified
		}
	}

	// Any remaining active events should be deleted
	deletedEvents := make([]string, 0, len(activeEvents))
	for event := range activeEvents {
		deletedEvents = append(deletedEvents, event)
	}

	log.Printf("Inserting %d events\n", len(newEvents))
	log.Printf("Updating %d events\n", len(updatedEvents))
	log.Printf("Deleting %d events\n", len(deletedEvents))

	err = bulkInsert(tx, newEvents)
	if err != nil {
		return err
	}
	err = bulkUpdate(tx, updatedEvents)
	if err != nil {
		return err
	}
	err = bulkDelete(tx, deletedEvents)
	return err
}

func rangeSlice(min, max int) []interface{} {
	a := make([]interface{}, max-min+1)
	for i := range a {
		a[i] = min + i
	}
	return a
}

func eventFields() []string {
	return []string{
		"event_id",
		"year",
		"active",
		"org_group",
		"title",
		"short_description",
		"long_description",
		"event_type",
		"game_system",
		"rules_edition",
		"min_players",
		"max_players",
		"age_required",
		"experience_required",
		"materials_provided",
		"start_time",
		"duration",
		"end_time",
		"gm_names",
		"website",
		"email",
		"tournament",
		"round_number",
		"total_rounds",
		"min_play_time",
		"attendee_registration",
		"cost",
		"location",
		"room_name",
		"table_number",
		"special_category",
		"tickets_available",
		"last_modified",
		"short_category",
	}
}

func eventToDbFields(event *events.GenconEvent) []interface{} {

	return []interface{}{
		event.EventId,
		event.Year,
		event.Active,
		event.Group,
		event.Title,
		event.ShortDescription,
		event.LongDescription,
		event.EventType,
		event.GameSystem,
		event.RulesEdition,
		event.MinPlayers,
		event.MaxPlayers,
		event.AgeRequired,
		event.ExperienceRequired,
		event.MaterialsProvided,
		event.StartTime,
		event.Duration,
		event.EndTime,
		event.GMNames,
		event.Website,
		event.Email,
		event.Tournament,
		event.RoundNumber,
		event.TotalRounds,
		event.MinPlayTime,
		event.AttendeeRegistration,
		event.Cost,
		event.Location,
		event.RoomName,
		event.TableNumber,
		event.SpecialCategory,
		event.TicketsAvailable,
		event.LastModified,
		event.ShortCategory,
	}
}

func scanEvent(row *sql.Rows) (*events.GenconEvent, error) {
	var event events.GenconEvent

	err := row.Scan(
		&event.EventId,
		&event.Year,
		&event.Active,
		&event.Group,
		&event.Title,
		&event.ShortDescription,
		&event.LongDescription,
		&event.EventType,
		&event.GameSystem,
		&event.RulesEdition,
		&event.MinPlayers,
		&event.MaxPlayers,
		&event.AgeRequired,
		&event.ExperienceRequired,
		&event.MaterialsProvided,
		&event.StartTime,
		&event.Duration,
		&event.EndTime,
		&event.GMNames,
		&event.Website,
		&event.Email,
		&event.Tournament,
		&event.RoundNumber,
		&event.TotalRounds,
		&event.MinPlayTime,
		&event.AttendeeRegistration,
		&event.Cost,
		&event.Location,
		&event.RoomName,
		&event.TableNumber,
		&event.SpecialCategory,
		&event.TicketsAvailable,
		&event.LastModified,
		&event.ShortCategory,
		&event.IsStarred)

	event.StartTime = event.StartTime.In(INDIANAPOLIS)
	event.EndTime = event.EndTime.In(INDIANAPOLIS)
	return &event, err
}

func bulkUpdate(tx *sql.Tx, updatedRows []*events.GenconEvent) error {
	eventFields := eventFields()
	numEventFields := len(eventFields)

	for _, row := range updatedRows {
		whereClause := fmt.Sprintf(
			"(%s) = %s",
			strings.Join(eventFields, ", "),
			fmt.Sprintf(
				"($%d"+strings.Repeat(", $%d", numEventFields-1)+")",
				rangeSlice(1, numEventFields)...))
		updateStatement := fmt.Sprintf(
			"UPDATE events SET %s WHERE event_id='%s'",
			whereClause,
			row.EventId)

		valueArgs := eventToDbFields(row)
		_, err := tx.Exec(updateStatement, valueArgs...)

		if err != nil {
			log.Printf("Error on updating event: %v %v", row, err.(pq.PGError))
			return err
		}
	}

	return nil
}

func bulkInsert(tx *sql.Tx, newRows []*events.GenconEvent) error {
	batchSize := 100

	eventFields := eventFields()
	numEventFields := len(eventFields)

	for len(newRows) > 0 {
		if batchSize > len(newRows) {
			// This is the final batch
			batchSize = len(newRows)
		}
		batch := newRows[0:batchSize:batchSize]
		newRows = newRows[batchSize:]

		valueStrings := make([]string, 0, len(batch))
		valueArgs := make([]interface{}, 0, len(batch)*numEventFields)
		for i, row := range batch {
			valueStrings = append(
				valueStrings,
				fmt.Sprintf(
					"( $%d "+strings.Repeat(",$%d", numEventFields-1)+" )",
					rangeSlice(i*numEventFields+1, i*numEventFields+numEventFields)...))
			valueArgs = append(valueArgs, eventToDbFields(row)...)
		}
		insertStatement := fmt.Sprintf(
			"INSERT INTO events (%s) VALUES %s",
			strings.Join(eventFields, ","),
			strings.Join(valueStrings, ","))
		_, err := tx.Exec(insertStatement, valueArgs...)

		if err != nil {
			log.Printf("Error on processing event: %s %v", batch, err.(pq.PGError))
			return err
		}
	}

	return nil
}
