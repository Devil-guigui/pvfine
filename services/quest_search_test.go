package services

import (
	"fmt"
	"strings"
	"testing"

	"pvfine/internal/pvf"
)

func TestQuestTargetSearchScopesBeforePagination(t *testing.T) {
	for _, disk := range []bool{false, true} {
		t.Run(fmt.Sprintf("disk=%v", disk), func(t *testing.T) {
			c, _, _ := editFixture(t, editableShop)
			for _, kind := range []string{"dungeon", "monster", "npc"} {
				extension := map[string]string{"dungeon": "dgn", "monster": "mob", "npc": "npc"}[kind]
				listPath := kind + "/" + kind + ".lst"
				text := fmt.Sprintf("101 `sub/one.%s` 102 `sub/two.%s`", extension, extension)
				if i, ok := c.archive.Find(listPath); ok {
					if err := c.archive.SetText(i, text); err != nil {
						t.Fatal(err)
					}
				} else if _, err := c.archive.AddFileText(listPath, text, pvf.TypeScript); err != nil {
					t.Fatal(err)
				}
				for _, entry := range []string{"one", "two", "unregistered"} {
					if _, err := c.archive.AddFileText(kind+"/sub/"+entry+"."+extension, "[name]\n`共同搜索名 "+entry+"`", pvf.TypeScript); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := c.archive.AddFileText(kind+"other/false."+extension, "[name]\n`共同搜索名`", pvf.TypeScript); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.setArchive(c.archive); err != nil {
				t.Fatal(err)
			}
			if disk {
				index, _, err := openSQLiteArchiveIndex(c.archive)
				if err != nil {
					t.Fatal(err)
				}
				c.mu.Lock()
				c.diskIndex = index
				c.installDiskArchiveIndexesLocked(c.archive)
				c.mu.Unlock()
			}
			c.startSearchIndex()
			waitForSearchIndex(t, c)
			service := NewArchiveService(c)
			for _, kind := range []string{"dungeon", "monster", "npc"} {
				for _, query := range []string{"共同搜索名", "10", "sub/", "*共同*"} {
					ids := map[string]bool{}
					cursor := 0
					for page := 0; page < 20; page++ {
						result, err := service.SearchQuestTargets(kind, query, cursor, 1)
						if err != nil {
							t.Fatal(err)
						}
						for _, hit := range result.Hits {
							if hit.ID == "" || !strings.HasPrefix(hit.Path, kind+"/") || ids[hit.ID] {
								t.Fatalf("unexpected result kind=%s query=%s: %#v", kind, query, hit)
							}
							ids[hit.ID] = true
						}
						if result.NextCursor < 0 {
							break
						}
						if result.NextCursor <= cursor {
							t.Fatal("cursor did not advance")
						}
						cursor = result.NextCursor
					}
					if len(ids) != 2 || !ids["101"] || !ids["102"] {
						t.Fatalf("kind=%s query=%s IDs=%v", kind, query, ids)
					}
				}
			}
			if _, err := service.SearchQuestTargets("equipment", "共同搜索名", 0, 30); err == nil {
				t.Fatal("unsupported kind accepted")
			}
			empty, err := service.SearchQuestTargets("npc", "", 0, 30)
			if err != nil || len(empty.Hits) != 0 || empty.NextCursor != -1 {
				t.Fatalf("empty query=%#v, %v", empty, err)
			}
		})
	}
}
