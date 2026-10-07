// Command generate writes the fixed, self-contained pure benchmark.
package main

import (
	"encoding/json"
	"fmt"
	"math/big"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

type problem struct {
	ID       string `json:"id"`
	Prompt   string `json:"prompt"`
	Expected string `json:"expected"`
	Checker  string `json:"checker"`
}

func main() {
	raw, err := json.MarshalIndent(problems(), "", "  ")
	if err == nil {
		err = os.WriteFile("bench/pure/problems.json", append(raw, '\n'), 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func problems() []problem {
	r := rand.New(rand.NewPCG(20261007, 51))
	return []problem{arithmetic(r), calendar(), sorting(r), bases(r), occurrences(r), csv(r), document(r), primes(), distance(r), brainfuck(r)}
}

func exact(id, prompt, answer string) problem { return problem{id, prompt, answer, "exact"} }

func digits(r *rand.Rand, n int, alphabet string) string {
	var b strings.Builder
	for range n {
		b.WriteByte(alphabet[r.IntN(len(alphabet))])
	}
	return b.String()
}

func integer(s string, base int) *big.Int {
	n, ok := new(big.Int).SetString(s, base)
	if !ok {
		panic("invalid integer")
	}
	return n
}

func arithmetic(r *rand.Rand) problem {
	a, b := digits(r, 40, "123456789"), digits(r, 40, "123456789")
	modulus := "984738291748392817483921"
	product := new(big.Int).Mul(integer(a, 10), integer(b, 10))
	rem := new(big.Int).Mod(new(big.Int).MulRange(1, 30), integer(modulus, 10))
	return exact("arithmetic", fmt.Sprintf("What is %s multiplied by %s, exactly? Also, what is the remainder when 30! is divided by %s? Reply with just the product and the remainder, separated by a comma and no spaces.", a, b, modulus), product.String()+","+rem.String())
}

func calendar() problem {
	a := time.Date(1643, 2, 17, 0, 0, 0, 0, time.UTC)
	b := time.Date(2387, 11, 29, 0, 0, 0, 0, time.UTC)
	weekday := time.Date(2199, 3, 1, 0, 0, 0, 0, time.UTC).Weekday().String()
	days := (b.Unix() - a.Unix()) / 86400
	return exact("calendar", "Using the proleptic Gregorian calendar, what weekday is March 1, 2199, and how many days elapse from February 17, 1643 to November 29, 2387? Reply only as Weekday,days with no spaces.", fmt.Sprintf("%s,%d", weekday, days))
}

func sorting(r *rand.Rand) problem {
	nums := make([]int, 500)
	parts := make([]string, len(nums))
	for i := range nums {
		nums[i] = r.IntN(2000001) - 1000000
		parts[i] = strconv.Itoa(nums[i])
	}
	slices.Sort(nums)
	sum := 0
	for i := 49; i < len(nums); i += 50 {
		sum += nums[i]
	}
	return exact("sort", "Sort these 500 integers in ascending order, retaining duplicates. Sum the elements at 1-based positions 50, 100, 150, 200, 250, 300, 350, 400, 450, and 500. Reply with just the sum.\n\n"+strings.Join(parts, ", "), strconv.Itoa(sum))
}

func bases(r *rand.Rand) problem {
	a := digits(r, 45, "123456789")
	b := digits(r, 36, "0123456789ABCDEF")
	c := "1" + digits(r, 140, "01")
	prompt := fmt.Sprintf("Convert the decimal number %s to uppercase hexadecimal, the hexadecimal number %s to decimal, and the binary number %s to decimal. Reply with only the three results in that order, separated by commas and no spaces, without base prefixes.", a, b, c)
	return exact("bases", prompt, strings.ToUpper(integer(a, 10).Text(16))+","+integer(b, 16).String()+","+integer(c, 2).String())
}

func occurrences(r *rand.Rand) problem {
	var b strings.Builder
	for b.Len() < 3072 {
		b.WriteString(digits(r, r.IntN(30)+5, "abxyz "))
		b.WriteString([]string{"ababa", "aba", "abababa", "baab"}[r.IntN(4)])
	}
	text := b.String()[:3072]
	count := 0
	for i := 0; i+3 <= len(text); i++ {
		if text[i:i+3] == "aba" {
			count++
		}
	}
	return exact("pattern", "How many exact occurrences of aba are in the text below? Count overlapping occurrences too. Reply with only the count. The text begins after BEGIN and ends before END; the markers are not part of it.\nBEGIN\n"+text+"\nEND", strconv.Itoa(count))
}

func csv(r *rand.Rand) problem {
	var b strings.Builder
	b.WriteString("group,amount\n")
	groups := []string{"amber", "blue", "coral", "dune"}
	sums := make([]int, 4)
	maximum := -100000
	for range 200 {
		g, v := r.IntN(4), r.IntN(20001)-10000
		sums[g] += v
		maximum = max(maximum, v)
		fmt.Fprintf(&b, "%s,%d\n", groups[g], v)
	}
	answer := make([]string, 0, 5)
	for i, g := range groups {
		answer = append(answer, fmt.Sprintf("%s=%d", g, sums[i]))
	}
	answer = append(answer, fmt.Sprintf("max=%d", maximum))
	return exact("csv", "For this CSV, sum amount for each group and find the largest individual amount across all rows. Reply only as amber=SUM,blue=SUM,coral=SUM,dune=SUM,max=VALUE, with no spaces.\n\n"+b.String(), strings.Join(answer, ","))
}

type item struct {
	ID     string `json:"id"`
	Active bool   `json:"active"`
	Region string `json:"region"`
	Qty    int    `json:"qty"`
	Price  int    `json:"price"`
}

func document(r *rand.Rand) problem {
	items := make([]item, 80)
	total := 0
	for i := range items {
		items[i] = item{fmt.Sprintf("item-%03d", i), r.IntN(3) != 0, []string{"north", "south", "east"}[r.IntN(3)], r.IntN(90) + 1, r.IntN(9000) + 100}
		v := items[i]
		if v.Active && v.Region == "north" {
			total += v.Qty * v.Price
		}
	}
	raw, err := json.Marshal(map[string]any{"items": items, "version": 3})
	if err != nil {
		panic(err)
	}
	return exact("json", "Transform this JSON document: retain only items whose active field is true and region is north; add a revenue field equal to qty times price to each retained item; set a top-level totalRevenue field to the sum of those revenues. What is totalRevenue? Reply with just that integer.\n\n"+string(raw), strconv.Itoa(total))
}

func primeCount(n int) int {
	composite := make([]bool, n)
	count := 0
	for i := 2; i < n; i++ {
		if composite[i] {
			continue
		}
		count++
		for j := i * 2; j < n; j += i {
			composite[j] = true
		}
	}
	return count
}

func primes() problem {
	return exact("primes", "How many prime numbers are strictly less than 199933? Reply with just the count.", strconv.Itoa(primeCount(199933)))
}

func levenshtein(a, b string) int {
	row := make([]int, len(b)+1)
	for j := range row {
		row[j] = j
	}
	for i := range len(a) {
		next := make([]int, len(b)+1)
		next[0] = i + 1
		for j := range len(b) {
			cost := 0
			if a[i] != b[j] {
				cost = 1
			}
			next[j+1] = min(next[j]+1, row[j+1]+1, row[j]+cost)
		}
		row = next
	}
	return row[len(b)]
}

func distance(r *rand.Rand) problem {
	a := digits(r, 62, "abcdefghijklmnpqrstuvwxyz")
	b := []byte(a)
	for range 25 {
		b[r.IntN(len(b))] = "abcdefghijklmnpqrstuvwxyz"[r.IntN(24)]
	}
	b = append(b[:19], b[22:]...)
	b = append(b[:45], append([]byte("qrx"), b[45:]...)...)
	return exact("distance", fmt.Sprintf("What is the Levenshtein distance between these two strings, with insertion, deletion, and substitution each costing 1? Reply with only the distance.\nFirst: %s\nSecond: %s", a, b), strconv.Itoa(levenshtein(a, string(b))))
}

func brainfuck(r *rand.Rand) problem {
	var b strings.Builder
	b.WriteString("++++++++[>++++++++<-]>")
	current := 64
	for range 14 {
		next := 65 + r.IntN(26)
		op := "+"
		if next < current {
			op = "-"
		}
		b.WriteString(strings.Repeat(op, abs(next-current)))
		b.WriteByte('.')
		current = next
	}
	code := b.String()
	return exact("brainfuck", "What does this Brainfuck program output? Cells are initially zero, are unsigned 8-bit values wrapping modulo 256, and the pointer starts at cell 0 on a tape extending to the right. There is no input. Reply with only the output characters.\n\n"+code, bfOutput(code))
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func bfOutput(code string) string {
	tape := make([]byte, 100)
	pointer := 0
	stack := []int{}
	jumps := map[int]int{}
	for i, c := range code {
		if c == '[' {
			stack = append(stack, i)
		}
		if c == ']' {
			j := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			jumps[i] = j
			jumps[j] = i
		}
	}
	var b strings.Builder
	for pc := 0; pc < len(code); pc++ {
		switch code[pc] {
		case '>':
			pointer++
		case '<':
			pointer--
		case '+':
			tape[pointer]++
		case '-':
			tape[pointer]--
		case '.':
			b.WriteByte(tape[pointer])
		case '[':
			if tape[pointer] == 0 {
				pc = jumps[pc]
			}
		case ']':
			if tape[pointer] != 0 {
				pc = jumps[pc]
			}
		}
	}
	return b.String()
}
