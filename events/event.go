package events

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"
	"unicode"
)

func CategoryFromEvent(rawEventId string) string {
	category, _ := splitId(rawEventId)
	return category
}

func YearFromEvent(rawEventId string) int {
	_, year := splitId(rawEventId)
	return year
}

func splitId(rawEventId string) (string, int) {
	// Remove the letters on the left leaves us with <2 # year><id>
	yearId := strings.TrimLeftFunc(rawEventId, unicode.IsLetter)
	// Remove the numbers on the right leaves us with the event category
	category := strings.TrimRightFunc(rawEventId, unicode.IsDigit)

	twoDigitYear, err := strconv.Atoi(yearId[:2])
	if err != nil {
		log.Fatalf("Unable to parse year out of %s, %v", rawEventId, err)
	}
	if 15 > twoDigitYear || 19 < twoDigitYear {
		log.Fatalf("Unsupported year being parsed! rawEventId %s", rawEventId)
	}

	return category, 2000 + twoDigitYear
}

type SlimEvent struct {
	EventId          string
	StartTime        time.Time
	Duration         int
	EndTime          time.Time
	Location         string
	RoomName         string
	TableNumber      string
	TicketsAvailable int
}

type GenconEvent struct {
	EventId              string
	Year                 int
	Active               bool
	Group                string
	Title                string
	ShortDescription     string
	LongDescription      string
	EventType            string
	GameSystem           string
	RulesEdition         string
	MinPlayers           int
	MaxPlayers           int
	AgeRequired          string
	ExperienceRequired   string
	MaterialsProvided    bool
	StartTime            time.Time
	Duration             int
	EndTime              time.Time
	GMNames              string
	Website              string
	Email                string
	Tournament           bool
	RoundNumber          int
	TotalRounds          int
	MinPlayTime          int
	AttendeeRegistration string
	Cost                 int
	Location             string
	RoomName             string
	TableNumber          string
	SpecialCategory      string
	TicketsAvailable     int
	LastModified         time.Time
	ShortCategory        string
}

func (e *GenconEvent) GenconLink() string {
	id := strings.TrimLeftFunc(e.EventId, unicode.IsLetter)[2:]
	return fmt.Sprintf("http://gencon.com/events/%v", id)
}

func (e *GenconEvent) SlimEvent() *SlimEvent {
	return &SlimEvent{
		EventId:          e.EventId,
		StartTime:        e.StartTime,
		Duration:         e.Duration,
		EndTime:          e.EndTime,
		Location:         e.Location,
		RoomName:         e.RoomName,
		TableNumber:      e.TableNumber,
		TicketsAvailable: e.TicketsAvailable,
	}
}
