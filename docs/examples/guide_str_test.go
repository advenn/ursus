package examples_test

import (
	"github.com/advenn/ursus"
)

// people is the strings chapter's frame: names in mixed case, one accented, and
// contact details in free text.
func people() *ursus.LazyFrame {
	return ursus.Frame(
		ursus.Values("name", []string{
			"  ada LOVELACE",
			"Zoë Smith ",
			"ben o'brien",
		}),
		ursus.Values("contact", []string{
			"ada@example.com, +44 20 7946 0958",
			"zoe.smith@mail.example.org",
			"call 555-0100 or 555-0199",
		}),
		ursus.Values("joined", []string{"2024-03-01", "2025-11-17", "bad date"}),
	)
}

// --- the user guide: strings ---------------------------------------------------------

func Example_strClean() {
	name := ursus.Col("name").Str()
	trimmed := name.StripChars(" ").Str() // a method gives an Expr: Str() again
	show(people().Select(
		trimmed.ToTitlecase().Alias("name"),
		trimmed.ToLower().Alias("lower"),
		name.LenChars().Alias("chars"),
		name.LenBytes().Alias("bytes"),
	))
	// Output:
	// shape: (3, 4)
	// ┌────────────────┬────────────────┬────────┬────────┐
	// │ name           │ lower          │ chars  │ bytes  │
	// │ String         │ String         │ Uint32 │ Uint32 │
	// ├────────────────┼────────────────┼────────┼────────┤
	// │ "Ada Lovelace" │ "ada lovelace" │ 14     │ 14     │
	// │ "Zoë Smith"    │ "zoë smith"    │ 10     │ 11     │
	// │ "Ben O'Brien"  │ "ben o'brien"  │ 11     │ 11     │
	// └────────────────┴────────────────┴────────┴────────┘
}

func Example_strMatch() {
	contact := ursus.Col("contact").Str()
	show(people().Select(
		ursus.Col("contact"),
		contact.Contains("@", true).Alias("email"),
		contact.Contains(`\d{3}-\d{4}`, false).Alias("us_phone"),
		contact.EndsWith(".org").Alias("org"),
		contact.CountMatches(`\d`, false).Alias("digits"),
	))
	// Output:
	// shape: (3, 5)
	// ┌─────────────────────────────────────┬───────┬──────────┬───────┬────────┐
	// │ contact                             │ email │ us_phone │ org   │ digits │
	// │ String                              │ Bool  │ Bool     │ Bool  │ Uint32 │
	// ├─────────────────────────────────────┼───────┼──────────┼───────┼────────┤
	// │ "ada@example.com, +44 20 7946 0958" │ true  │ false    │ false │ 12     │
	// │ "zoe.smith@mail.example.org"        │ true  │ false    │ true  │ 0      │
	// │ "call 555-0100 or 555-0199"         │ false │ true     │ false │ 14     │
	// └─────────────────────────────────────┴───────┴──────────┴───────┴────────┘
}

func Example_strExtract() {
	contact := ursus.Col("contact").Str()
	show(people().Select(
		contact.Extract(`([\w.]+)@([\w.]+)`, 2).Alias("domain"),
		contact.ExtractAll(`\d{3}-\d{4}`).Alias("phones"),
	))
	// Output:
	// shape: (3, 2)
	// ┌────────────────────┬──────────────────────────┐
	// │ domain             │ phones                   │
	// │ String             │ List(String)             │
	// ├────────────────────┼──────────────────────────┤
	// │ "example.com"      │ []                       │
	// │ "mail.example.org" │ []                       │
	// │ null               │ ["555-0100", "555-0199"] │
	// └────────────────────┴──────────────────────────┘
}

func Example_strReplace() {
	contact := ursus.Col("contact").Str()
	show(people().Select(
		contact.Replace(`\d`, "#", false).Alias("first_digit"),
		contact.ReplaceAll(`\d`, "#", false).Alias("every_digit"),
	))
	// Output:
	// shape: (3, 2)
	// ┌─────────────────────────────────────┬─────────────────────────────────────┐
	// │ first_digit                         │ every_digit                         │
	// │ String                              │ String                              │
	// ├─────────────────────────────────────┼─────────────────────────────────────┤
	// │ "ada@example.com, +#4 20 7946 0958" │ "ada@example.com, +## ## #### ####" │
	// │ "zoe.smith@mail.example.org"        │ "zoe.smith@mail.example.org"        │
	// │ "call #55-0100 or 555-0199"         │ "call ###-#### or ###-####"         │
	// └─────────────────────────────────────┴─────────────────────────────────────┘
}

func Example_strSplit() {
	show(people().Select(
		ursus.Col("name").Str().StripChars(" ").Str().Split(" ").Alias("words"),
	))
	show(people().
		Select(ursus.Col("joined").Str().SplitExact("-", 2, false).Alias("parts")).
		Unnest("parts"))
	// Output:
	// shape: (3, 1)
	// ┌─────────────────────┐
	// │ words               │
	// │ List(String)        │
	// ├─────────────────────┤
	// │ ["ada", "LOVELACE"] │
	// │ ["Zoë", "Smith"]    │
	// │ ["ben", "o'brien"]  │
	// └─────────────────────┘
	//
	// shape: (3, 3)
	// ┌────────────┬─────────┬─────────┐
	// │ field_0    │ field_1 │ field_2 │
	// │ String     │ String  │ String  │
	// ├────────────┼─────────┼─────────┤
	// │ "2024"     │ "03"    │ "01"    │
	// │ "2025"     │ "11"    │ "17"    │
	// │ "bad date" │ null    │ null    │
	// └────────────┴─────────┴─────────┘
}

func Example_strSlice() {
	code := ursus.Frame(ursus.Values("code", []string{"7", "42", "1234"}))
	c := ursus.Col("code").Str()
	show(code.Select(
		ursus.Col("code"),
		c.ZFill(5).Alias("zfill"),
		c.PadEnd(5, ".").Alias("pad_end"),
		c.Head(2).Alias("head"),
		c.Slice(1, 2).Alias("slice"),
		c.Reverse().Alias("reverse"),
	))
	// Output:
	// shape: (3, 6)
	// ┌────────┬─────────┬─────────┬────────┬────────┬─────────┐
	// │ code   │ zfill   │ pad_end │ head   │ slice  │ reverse │
	// │ String │ String  │ String  │ String │ String │ String  │
	// ├────────┼─────────┼─────────┼────────┼────────┼─────────┤
	// │ "7"    │ "00007" │ "7...." │ "7"    │ ""     │ "7"     │
	// │ "42"   │ "00042" │ "42..." │ "42"   │ "2"    │ "24"    │
	// │ "1234" │ "01234" │ "1234." │ "12"   │ "23"   │ "4321"  │
	// └────────┴─────────┴─────────┴────────┴────────┴─────────┘
}

func Example_strParse() {
	joined := ursus.Col("joined").Str()
	show(people().Select(
		ursus.Col("joined"),
		joined.Strptime(ursus.Date, "%Y-%m-%d", false).Alias("date"),
		joined.Head(4).Str().ToInteger().Alias("year"),
	))
	// Output:
	// shape: (3, 3)
	// ┌──────────────┬────────────┬───────┐
	// │ joined       │ date       │ year  │
	// │ String       │ Date       │ Int64 │
	// ├──────────────┼────────────┼───────┤
	// │ "2024-03-01" │ 2024-03-01 │ 2024  │
	// │ "2025-11-17" │ 2025-11-17 │ 2025  │
	// │ "bad date"   │ null       │ null  │
	// └──────────────┴────────────┴───────┘
}

func Example_strCombine() {
	show(sales().Head(3).Select(
		ursus.ConcatStr("/", ursus.Col("region"), ursus.Col("product")).Alias("path"),
		ursus.Format("{} x {}", ursus.Col("qty"), ursus.Col("product")).Alias("line"),
	))
	show(sales().
		GroupBy(ursus.Col("region")).
		Agg(ursus.Col("product").Str().Join(", ").Alias("products")).
		Sort(ursus.Asc(ursus.Col("region"))))
	// Output:
	// shape: (3, 2)
	// ┌───────────────┬──────────────┐
	// │ path          │ line         │
	// │ String        │ String       │
	// ├───────────────┼──────────────┤
	// │ "north/apple" │ "10 x apple" │
	// │ "south/apple" │ "4 x apple"  │
	// │ "north/pear"  │ "7 x pear"   │
	// └───────────────┴──────────────┘
	//
	// shape: (3, 2)
	// ┌─────────┬────────────────────┐
	// │ region  │ products           │
	// │ String  │ String             │
	// ├─────────┼────────────────────┤
	// │ "east"  │ "pear, apple"      │
	// │ "north" │ "apple, pear, fig" │
	// │ "south" │ "apple, fig, pear" │
	// └─────────┴────────────────────┘
}
