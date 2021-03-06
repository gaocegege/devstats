package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"sort"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"

	lib "devstats"
)

// dashboard stores main dashoard keys title and uid
type dashboard struct {
	Title string   `json:"title"`
	UID   string   `json:"uid"`
	Tags  []string `json:"tags"`
}

// dashboardData keeps all dashboard data & metadata
type dashboardData struct {
	dash  dashboard
	id    int
	title string
	slug  string
	data  string
	fn    string
}

// String for dashboardData - skip displaying long JSON data
func (dd dashboardData) String() string {
	return fmt.Sprintf(
		"{dash:'%+v', id:%d, title:'%s', slug:'%s', data:len:%d, fn:'%s'}",
		dd.dash, dd.id, dd.title, dd.slug, len(dd.data), dd.fn,
	)
}

// updateTags make JSON and SQLite tags match each other
func updateTags(db *sql.DB, ctx *lib.Ctx, did int, jsonTags []string, info string) bool {
	// Get SQLite DB tags
	rows, err := db.Query(
		"select term from dashboard_tag where dashboard_id = ? order by term asc",
		did,
	)
	lib.FatalOnError(err)
	defer func() { lib.FatalOnError(rows.Close()) }()
	tag := ""
	dbTags := []string{}
	for rows.Next() {
		lib.FatalOnError(rows.Scan(&tag))
		dbTags = append(dbTags, tag)
	}
	lib.FatalOnError(rows.Err())

	// Sort jsonTags
	sort.Strings(jsonTags)
	sJSONTags := strings.Join(jsonTags, ",")
	sDBTags := strings.Join(dbTags, ",")
	// If the same tag set, return false - meaning no update was needed
	if sJSONTags == sDBTags {
		return false
	}

	// Now sync tags
	allMap := make(map[string]struct{})
	dbMap := make(map[string]struct{})
	jsonMap := make(map[string]struct{})
	for _, tag := range jsonTags {
		jsonMap[tag] = struct{}{}
		allMap[tag] = struct{}{}
	}
	for _, tag := range dbTags {
		dbMap[tag] = struct{}{}
		allMap[tag] = struct{}{}
	}
	nI := 0
	nD := 0
	for tag := range allMap {
		_, j := jsonMap[tag]
		_, d := dbMap[tag]
		// We have it in JSOn but not in DB, insert
		if j && !d {
			_, err = db.Exec(
				"insert into dashboard_tag(dashboard_id, term) values(?, ?)",
				did, tag,
			)
			lib.FatalOnError(err)
			if ctx.Debug > 0 {
				lib.Printf(
					"Updating dashboard '%s' id: %d, '%v' -> '%v', inserted '%s' tag\n",
					info, did, sDBTags, sJSONTags, tag,
				)
			}
			nI++
		}
		// We have it in DB but not in JSON, delete
		if !j && d {
			_, err = db.Exec(
				"delete from dashboard_tag where dashboard_id = ? and term = ?",
				did, tag,
			)
			lib.FatalOnError(err)
			if ctx.Debug > 0 {
				lib.Printf(
					"Updating dashboard '%s' id: %d, '%v' -> '%v', deleted '%s' tag\n",
					info, did, sDBTags, sJSONTags, tag,
				)
			}
			nD++
		}
	}
	lib.Printf(
		"Updated dashboard '%s' id: %d, '%v' -> '%v', added: %d, removed: %d\n",
		info, did, sDBTags, sJSONTags, nI, nD,
	)
	return true
}

// exportJsons uses dbFile database to dump all dashboards as JSONs
func exportJsons(ictx *lib.Ctx, dbFile string) {
	// Connect to SQLite3
	db, err := sql.Open("sqlite3", dbFile)
	lib.FatalOnError(err)
	defer func() { lib.FatalOnError(db.Close()) }()

	// Get all dashboards
	rows, err := db.Query("select slug, title, data from dashboard")
	lib.FatalOnError(err)
	defer func() { lib.FatalOnError(rows.Close()) }()
	var (
		slug  string
		title string
		data  string
	)
	// Save all of them as sqlite/slug[i].json for i=0..n
	for rows.Next() {
		lib.FatalOnError(rows.Scan(&slug, &title, &data))
		fn := "sqlite/" + slug + ".json"
		lib.FatalOnError(ioutil.WriteFile(fn, lib.PrettyPrintJSON([]byte(data)), 0644))
		lib.Printf("Written '%s' to %s\n", title, fn)
	}
	lib.FatalOnError(rows.Err())
}

// importJsonsByUID uses dbFile database to update list of JSONs
// It first loads all dashboards titles, slugs, ids and JSONs
// Then it parses all JSONs to get each dashboards UID
// Then it processes all JSONs provided, parses them, and gets each JSONs uid and title
// Each uid from JSON list must be unique
// Then for all JSON titles it creates slugs 'Name of Dashboard' -> 'name-of-dashboard'
// Finally it attempts to update SQLite database's data, tile, slug values by matching using UID
func importJsonsByUID(ctx *lib.Ctx, dbFile string, jsons []string) {
	// DB backup func, executed when anything is updated
	backedUp := false
	contents, err := lib.ReadFile(ctx, dbFile)
	lib.FatalOnError(err)
	backupFunc := func() {
		bfn := fmt.Sprintf("%s.%v", dbFile, time.Now().UnixNano())
		lib.FatalOnError(ioutil.WriteFile(bfn, contents, 0644))
		lib.Printf("Original db file backed up as' %s'\n", bfn)
	}

	// Connect to SQLite3
	db, err := sql.Open("sqlite3", dbFile)
	lib.FatalOnError(err)
	defer func() { lib.FatalOnError(db.Close()) }()

	// Load and parse all dashboards JSONs
	// Will keep uid --> sqlite dashboard data map
	dbMap := make(map[string]dashboardData)
	rows, err := db.Query("select id, data, title, slug from dashboard")
	lib.FatalOnError(err)
	defer func() { lib.FatalOnError(rows.Close()) }()
	for rows.Next() {
		var dd dashboardData
		lib.FatalOnError(rows.Scan(&dd.id, &dd.data, &dd.title, &dd.slug))
		lib.FatalOnError(json.Unmarshal([]byte(dd.data), &dd.dash))
		if dd.title != dd.dash.Title {
			lib.Fatalf("SQLite internal inconsistency: %s != %s", dd.title, dd.dash.Title)
		}
		dd.data = string(lib.PrettyPrintJSON([]byte(dd.data)))
		dd.fn = "*" + dd.slug + ".json*"
		dbMap[dd.dash.UID] = dd
	}
	lib.FatalOnError(rows.Err())
	nDbMap := len(dbMap)

	// Now load & parse JSON arguments
	jsonMap := make(map[string]dashboardData)
	for _, j := range jsons {
		var dd dashboardData
		bytes, err := lib.ReadFile(ctx, j)
		lib.FatalOnError(err)
		lib.FatalOnError(json.Unmarshal(bytes, &dd.dash))
		dbDash, ok := dbMap[dd.dash.UID]
		if !ok {
			lib.Fatalf("%s: uid=%s not found in SQLite, attempted to import '%s'", j, dd.dash.UID, dd.dash.Title)
		}
		jsonDash, ok := jsonMap[dd.dash.UID]
		if ok {
			lib.Fatalf("%s: duplicate json uid, attempt to import %v, collision with %v", j, dd.dash, jsonDash.dash)
		}
		dd.data = string(lib.PrettyPrintJSON(bytes))
		dd.id = dbDash.id
		dd.title = dd.dash.Title
		dd.slug = lib.Slugify(dd.title)
		dd.fn = j
		jsonMap[dd.dash.UID] = dd
	}
	nJSONMap := len(jsonMap)

	// Now do updates
	nImp := 0
	for uid, dd := range jsonMap {
		ddWas := dbMap[uid]
		if ctx.Debug > 1 {
			lib.Printf("\n%+v\n%+v\n\n", dd.String(), ddWas.String())
		}
		// Update/check tags
		updated := updateTags(db, ctx, dd.id, dd.dash.Tags, dd.dash.UID+" "+dd.dash.Title)

		// Check if we actually need to update anything
		if ddWas.dash.Title == dd.dash.Title && ddWas.slug == dd.slug && ddWas.data == dd.data {
			if updated {
				if !backedUp {
					backupFunc()
					backedUp = true
				}
				nImp++
			}
			continue
		}
		// Update JSON inside database
		_, err = db.Exec(
			"update dashboard set title = ?, slug = ?, data = ? where id = ?",
			dd.dash.Title, dd.slug, dd.data, dd.id,
		)
		lib.FatalOnError(err)

		// Info
		if ctx.Debug > 0 {
			lib.Printf(
				"%s: updated uid: %s: tags updated: %v\nnew: %+v\nold: %+v\n",
				dd.fn, uid, updated, dd, ddWas,
			)
		} else {
			lib.Printf(
				"%s: updated dashboard: uid: %s title: '%s' -> '%s', slug: '%s' -> '%s', tags: %v:%v (data %d -> %d bytes)\n",
				dd.fn, uid, ddWas.dash.Title, dd.dash.Title, ddWas.slug, dd.slug, updated, dd.dash.Tags, len(ddWas.data), len(dd.data),
			)
		}

		// And save JSON from DB
		lib.FatalOnError(ioutil.WriteFile(dd.fn+".was", []byte(ddWas.data), 0644))

		// Something changed, backup original db file
		if !backedUp {
			backupFunc()
			backedUp = true
		}
		nImp++
	}
	lib.Printf(
		"SQLite DB has %d dashboards, there were %d JSONs to import, imported %d\n",
		nDbMap, nJSONMap, nImp)
}

// importJsonsByTitle uses dbFile database to update list of JSONs
// each json can be either:
// 1) "filename.json"
// a) it will search for a SQLite dashboard with "title" the same as JSON's "title" property
// b) it will check if JSON's "uid" is the same as SQLite's dashboard JSON's "uid"
// c) it will udpate SQLite's dashboards "data" with new JSON
// d) SQLite's dashboard "title" and "slug" won't be changed
// 2) "filename.json;old title;new slug"
// a) it will search for a SQLite dashboard with "title" = "old title"
// b) it will check if JSON's "uid" is the same as SQLite's dashboard JSON's "uid"
// c) it will udpate SQLite's "data" with new JSON
// d) it will update SQLite's dashboard "title" with "title" property from filename.json
// e) it will update SQLite's dashboard "slug" = "new slug"
func importJsonsByTitle(ctx *lib.Ctx, dbFile string, jsons []string) {
	// DB backup func, executed when anything is updated
	backedUp := false
	contents, err := lib.ReadFile(ctx, dbFile)
	lib.FatalOnError(err)
	backupFunc := func() {
		bfn := fmt.Sprintf("%s.%v", dbFile, time.Now().UnixNano())
		lib.FatalOnError(ioutil.WriteFile(bfn, contents, 0644))
		lib.Printf("Original db file backed up as' %s'\n", bfn)
	}

	// Connect to SQLite3
	db, err := sql.Open("sqlite3", dbFile)
	lib.FatalOnError(err)
	defer func() { lib.FatalOnError(db.Close()) }()
	var (
		dash  dashboard
		dash2 dashboard
		data  string
		id    int
		slug  string
	)

	// Process JSONs
	for i, jdata := range jsons {
		// each jdata can be: "filename.json" or "filename.json;old title;new slug"
		ary := strings.Split(jdata, ";")
		j := ary[0]
		l := len(ary)
		if l != 1 && l != 3 {
			lib.Fatalf("you need to provide jsons either as 'filename.json' or as 'fn.json;old title;new slug'")
		}

		// Read JSON: get title & uid
		lib.Printf("Importing #%d json: %s (%v)\n", i+1, j, ary)
		bytes, err := lib.ReadFile(ctx, j)
		lib.FatalOnError(err)
		sBytes := string(bytes)
		lib.FatalOnError(json.Unmarshal(bytes, &dash))

		// Either use dashboard title from JSON or use "old title" provided from command line
		dashTitle := dash.Title
		if len(ary) > 1 {
			dashTitle = ary[1]
		}

		// Get original id, JSON, slug
		rows, err := db.Query("select id, data, slug from dashboard where title = ?", dashTitle)
		lib.FatalOnError(err)
		defer func() { lib.FatalOnError(rows.Close()) }()
		got := false
		for rows.Next() {
			lib.FatalOnError(rows.Scan(&id, &data, &slug))
			got = true
		}
		lib.FatalOnError(rows.Err())
		if !got {
			lib.Fatalf("dashboard titled: '%s' not found", dashTitle)
		}

		// Check UIDs
		lib.FatalOnError(json.Unmarshal([]byte(data), &dash2))
		if dash.UID != dash2.UID {
			lib.Printf("UID mismatch, json value: %s, database value: %s, skipping\n", dash.UID, dash2.UID)
			continue
		}

		// Update JSON inside database
		dashSlug := slug
		if len(ary) > 2 {
			dashSlug = ary[2]
		}
		_, err = db.Exec(
			"update dashboard set title = ?, slug = ?, data = ? where id = ?",
			dash.Title, dashSlug, sBytes, id,
		)
		lib.FatalOnError(err)
		updated := updateTags(db, ctx, id, dash.Tags, dash.UID+" "+dash.Title)

		if ctx.Debug > 0 {
			lib.Printf(
				"Updated (title: '%s' -> '%s', slug: '%s' -> '%s', tags: %v:%v):\n%s\nTo:\n%s\n",
				dashTitle, dash.Title, slug, dashSlug, updated, dash.Tags, data, sBytes,
			)
		} else {
			lib.Printf(
				"Updated dashboard: title: '%s' -> '%s', slug: '%s' -> '%s', tags: %v:%v\n",
				dashTitle, dash.Title, slug, dashSlug, updated, dash.Tags,
			)
		}

		// And save JSON from DB
		lib.FatalOnError(ioutil.WriteFile(j+".was", lib.PrettyPrintJSON([]byte(data)), 0644))

		//Something changed, backup original db file
		if !backedUp {
			backupFunc()
			backedUp = true
		}
	}
}

func main() {
	dtStart := time.Now()
	// Environment context parse
	var ctx lib.Ctx
	ctx.Init()

	if len(os.Args) < 2 {
		lib.Printf("Required args: grafana.db file name and list(*) of jsons to import.\n")
		lib.Printf("If only db file name given, it will output all dashboards to jsons\n")
		lib.Printf("Each list item can be either filename.json name or 'fn.json;old title;new slug'\n")
		lib.Printf("If special GHA2DB_UIDMODE is set, it will import JSONs by matching their internal uid with SQLite database\n")
		os.Exit(1)
	}
	if len(os.Args) > 2 {
		if ctx.UIDMode {
			importJsonsByUID(&ctx, os.Args[1], os.Args[2:])
		} else {
			importJsonsByTitle(&ctx, os.Args[1], os.Args[2:])
		}
	} else {
		exportJsons(&ctx, os.Args[1])
	}
	dtEnd := time.Now()
	lib.Printf("Time: %v\n", dtEnd.Sub(dtStart))
}
