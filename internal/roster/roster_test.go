package roster_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/USA-RedDragon/fah-dashboard/internal/roster"
)

func TestParseKeepsValidUniqueNames(t *testing.T) {
	t.Parallel()
	sheet := "Timestamp,LTT Forum Name ,F@H VALIDATION,Forum VALIDATION\n" +
		"1,alpha,VALID,VALID\n" +
		"2,beta,NOT FOUND,VALID\n" +
		"3,gamma,NO points to LTT,VALID\n" +
		"4, Alpha ,VALID,VALID\n" +
		"5,Scorching Arrow,VALID,No LTT Forum user Found\n"
	names, err := roster.Parse(strings.NewReader(sheet))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"alpha", "Scorching Arrow"}; !slices.Equal(names, want) {
		t.Errorf("names = %v", names)
	}
}

func TestParseKeepsEveryNameWithoutValidation(t *testing.T) {
	t.Parallel()
	sheet := "Rank,Change,LTT Forum Name ,Today's Points\n1,+2,HayesK,5\n2,,sazrocks,4\n,,,\n"
	names, err := roster.Parse(strings.NewReader(sheet))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"HayesK", "sazrocks"}; !slices.Equal(names, want) {
		t.Errorf("names = %v", names)
	}
}

func TestParseRejectsChangedSheet(t *testing.T) {
	t.Parallel()
	if _, err := roster.Parse(strings.NewReader("a,b\n1,2\n")); !errors.Is(err, roster.ErrBadSheet) {
		t.Errorf("err = %v", err)
	}
}
