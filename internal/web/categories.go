package web

import (
	"database/sql"
	"github.com/Encinarus/genconplanner/internal/postgres"
	"github.com/gin-gonic/gin"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func CategoryList(db *sql.DB) func(c *gin.Context) {
	return func(c *gin.Context) {
		defaultYear := time.Now().Year()

		var err error
		context := c.MustGet("context").(*Context)

		if len(strings.TrimSpace(c.Param("year"))) > 0 {
			context.Year, err = strconv.Atoi(c.Param("year"))
			if err != nil {
				log.Printf("Error parsing year")
				c.AbortWithError(http.StatusBadRequest, err)
				return
			}
		} else {
			context.Year = defaultYear
		}

		summary, err := postgres.LoadCategorySummary(db, context.Year)

		if err != nil {
			log.Printf("Error loading categories, %v", err)
			c.AbortWithError(500, err)
			return
		}

		batchSize := 2
		tail := len(summary) % batchSize
		numBuckets := len(summary) / batchSize
		if tail > 0 {
			numBuckets++
		}
		categories := make([][]*postgres.CategorySummary, numBuckets)
		for i := range categories {
			base := batchSize * i
			end := base + batchSize
			if i == len(categories)-1 {
				end = base + tail
			}
			categories[i] = summary[base:end]
		}
		log.Printf("Loaded %d categories in %d rows", len(summary), len(categories))
		c.HTML(http.StatusOK, "categories.html", gin.H{
			"title":      "Main website",
			"categories": categories,
			"context":    context,
		})
	}
}

func ViewCategory(db *sql.DB) func(c *gin.Context) {
	keyFunc := func(g *postgres.EventGroup) string {
		if len(strings.TrimSpace(g.GameSystem)) == 0 {
			return "Unspecified"
		}
		return g.GameSystem
	}
	return func(c *gin.Context) {
		appContext := c.MustGet("context").(*Context)
		var err error

		appContext.Year, err = strconv.Atoi(c.Param("year"))
		if err != nil {
			log.Printf("Error parsing year")
			c.AbortWithError(http.StatusBadRequest, err)
			return
		}
		cat := c.Param("cat")
		if len(strings.TrimSpace(cat)) == 0 {
			log.Printf("No category specified")
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}

		rawDays := c.Param("days")
		processedDays := make([]int, 0)
		splitDays := strings.Split(strings.ToLower(rawDays), ",")
		for _, day := range splitDays {
			switch day {
			case "sun":
				processedDays = append(processedDays, 0)
				break
			case "wed":
				processedDays = append(processedDays, 3)
				break
			case "thu":
				processedDays = append(processedDays, 4)
				break
			case "fri":
				processedDays = append(processedDays, 5)
				break
			case "sat":
				processedDays = append(processedDays, 6)
				break
			}
		}
		eventGroups, err := postgres.LoadEventGroups(db, cat, appContext.Year, processedDays)
		if err != nil {
			log.Printf("Error loading event groups")
			c.AbortWithError(http.StatusBadRequest, err)
			return
		}

		totalEvents := 0
		for _, group := range eventGroups {
			totalEvents += group.Count
		}

		headings, partitions := PartitionGroups(eventGroups, keyFunc)
		c.HTML(http.StatusOK, "results.html", gin.H{
			"context":     appContext,
			"headings":    headings,
			"partitions":  partitions,
			"totalEvents": totalEvents,
			"groups":      len(eventGroups),
			"breakdown":   "Systems",
			"pageHeader":  "Category",
			"subHeader":   cat,
		})
	}
}
