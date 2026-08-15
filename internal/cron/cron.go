// Package cron 实现标准 5 段 Cron 表达式的解析与下次执行时间计算。
//
// 仅依赖 Go 标准库。表达式为 "分 时 日 月 周" 五段，支持星号、数值、
// 区间、步长与逗号列表，以及月份（JAN-DEC）和星期（SUN-SAT）别名。
// 下次执行时间在基准时间的时区下逐字段匹配，严格晚于基准时间。
package cron

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// 解析与计算相关哨兵错误。
var (
	ErrEmptyExpr   = errors.New("表达式不能为空")
	ErrFieldCount  = errors.New("表达式必须恰好为 5 段：分 时 日 月 周")
	ErrEmptyField  = errors.New("字段或列表项不能为空")
	ErrBadValue    = errors.New("字段取值不是合法数字或别名")
	ErrOutOfRange  = errors.New("字段取值超出合法范围")
	ErrBadRange    = errors.New("区间起点不能大于终点")
	ErrBadStep     = errors.New("步长必须为正整数")
	ErrNoOccur     = errors.New("表达式在合理上界内无命中")
	ErrBadFromTime = errors.New("基准时间不是合法的 RFC3339 时间")
)

// monthNames 月份别名到数值（1-12）。
var monthNames = map[string]int{
	"JAN": 1, "FEB": 2, "MAR": 3, "APR": 4, "MAY": 5, "JUN": 6,
	"JUL": 7, "AUG": 8, "SEP": 9, "OCT": 10, "NOV": 11, "DEC": 12,
}

// dowNames 星期别名到数值（0=周日 … 6=周六）。
var dowNames = map[string]int{
	"SUN": 0, "MON": 1, "TUE": 2, "WED": 3, "THU": 4, "FRI": 5, "SAT": 6,
}

// searchLimit 自基准时间起的最长搜索跨度，超过即视为无命中。
const searchLimit = 5 * 365 * 24 * time.Hour

// Field 表示一个 cron 字段解析后的取值集合。
type Field struct {
	set  map[int]bool
	star bool // 原始字段是否为字面量 "*"
}

// Contains 报告某取值是否在集合中。
func (f Field) Contains(v int) bool { return f.set[v] }

// Values 返回有序取值列表，供外部展示。
func (f Field) Values() []int {
	out := make([]int, 0, len(f.set))
	for v := range f.set {
		out = append(out, v)
	}
	sortInts(out)
	return out
}

// Star 报告原始字段是否为字面量 "*"。
func (f Field) Star() bool { return f.star }

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}

// Expr 表示一个解析后的 Cron 表达式。
type Expr struct {
	Minute Field
	Hour   Field
	Dom    Field
	Month  Field
	Dow    Field
}

// Parse 将 5 段 Cron 表达式解析为 Expr，非法输入返回错误且不做静默修正。
func Parse(s string) (*Expr, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, ErrEmptyExpr
	}
	fields := strings.Fields(s)
	if len(fields) != 5 {
		return nil, ErrFieldCount
	}
	minute, err := parseField(fields[0], 0, 59, nil)
	if err != nil {
		return nil, fmt.Errorf("分钟字段: %w", err)
	}
	hour, err := parseField(fields[1], 0, 23, nil)
	if err != nil {
		return nil, fmt.Errorf("小时字段: %w", err)
	}
	dom, err := parseField(fields[2], 1, 31, nil)
	if err != nil {
		return nil, fmt.Errorf("日期字段: %w", err)
	}
	month, err := parseField(fields[3], 1, 12, monthNames)
	if err != nil {
		return nil, fmt.Errorf("月份字段: %w", err)
	}
	dow, err := parseField(fields[4], 0, 7, dowNames)
	if err != nil {
		return nil, fmt.Errorf("星期字段: %w", err)
	}
	// 星期中 7 等价于 0（周日）。
	if dow.set[7] {
		delete(dow.set, 7)
	}
	return &Expr{Minute: minute, Hour: hour, Dom: dom, Month: month, Dow: dow}, nil
}

func parseField(s string, min, max int, names map[string]int) (Field, error) {
	f := Field{set: make(map[int]bool), star: s == "*"}
	if s == "" {
		return f, ErrEmptyField
	}
	for _, part := range strings.Split(s, ",") {
		if part == "" {
			return f, ErrEmptyField
		}
		if err := f.addPart(part, min, max, names); err != nil {
			return f, err
		}
	}
	return f, nil
}

func (f *Field) addPart(part string, min, max int, names map[string]int) error {
	step := 1
	rangePart := part
	if i := strings.Index(part, "/"); i >= 0 {
		ss := part[i+1:]
		if ss == "" {
			return ErrBadStep
		}
		n, err := strconv.Atoi(ss)
		if err != nil || n <= 0 {
			return ErrBadStep
		}
		step = n
		rangePart = part[:i]
	}
	var lo, hi int
	switch {
	case rangePart == "*":
		lo, hi = min, max
	case strings.Contains(rangePart, "-"):
		a, b, err := parseRange(rangePart, min, max, names)
		if err != nil {
			return err
		}
		lo, hi = a, b
	default:
		v, err := resolveValue(rangePart, min, max, names)
		if err != nil {
			return err
		}
		lo = v
		if step != 1 {
			hi = max // a/n：从 a 到字段上限
		} else {
			hi = v
		}
	}
	for v := lo; v <= hi; v += step {
		f.set[v] = true
	}
	return nil
}

func parseRange(s string, min, max int, names map[string]int) (int, int, error) {
	parts := strings.SplitN(s, "-", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return 0, 0, ErrBadValue
	}
	lo, err := resolveValue(parts[0], min, max, names)
	if err != nil {
		return 0, 0, err
	}
	hi, err := resolveValue(parts[1], min, max, names)
	if err != nil {
		return 0, 0, err
	}
	if lo >= hi {
		return 0, 0, ErrBadRange
	}
	return lo, hi, nil
}

func resolveValue(s string, min, max int, names map[string]int) (int, error) {
	if names != nil {
		if v, ok := names[strings.ToUpper(s)]; ok {
			return v, nil
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, ErrBadValue
	}
	if n < min || n > max {
		return 0, ErrOutOfRange
	}
	return n, nil
}

// Next 返回严格晚于 from 的下一个匹配时刻；无命中返回 ErrNoOccur。
func (e *Expr) Next(from time.Time) (time.Time, error) {
	loc := from.Location()
	// 起始候选：from 所在分钟的下一分钟整（保证严格晚于 from）。
	t := time.Date(from.Year(), from.Month(), from.Day(), from.Hour(), from.Minute(), 0, 0, loc)
	if t.Before(from) {
		t = t.Add(time.Minute)
	}
	limit := from.Add(searchLimit)
	for t.Before(limit) {
		if !e.Month.Contains(int(t.Month())) {
			t = firstDayOfNextMonth(t, loc)
			continue
		}
		if !e.dayMatches(t) {
			t = midnightNextDay(t, loc)
			continue
		}
		if !e.Hour.Contains(t.Hour()) {
			t = topOfNextHour(t, loc)
			continue
		}
		if !e.Minute.Contains(t.Minute()) {
			t = t.Add(time.Minute)
			continue
		}
		return t, nil
	}
	return time.Time{}, ErrNoOccur
}

// dayMatches 按析取/合取规则判断某日是否命中：
// 日期与星期均非 * 时取 OR，否则取 AND。
func (e *Expr) dayMatches(t time.Time) bool {
	domMatch := e.Dom.Contains(t.Day())
	dowMatch := e.Dow.Contains(int(t.Weekday()))
	if !e.Dom.star && !e.Dow.star {
		return domMatch || dowMatch
	}
	return domMatch && dowMatch
}

func firstDayOfNextMonth(t time.Time, loc *time.Location) time.Time {
	return time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, loc)
}

func midnightNextDay(t time.Time, loc *time.Location) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
}

func topOfNextHour(t time.Time, loc *time.Location) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, loc)
}
