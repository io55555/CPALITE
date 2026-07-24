package main

import (
	"net/url"
	"strconv"
	"strings"
)

const (
	defaultPageSize = 80
	maxPageSize     = 200
)

type pageQuery struct {
	Page     int
	PageSize int
	Filter   string
	Q        string
	Status   int // bans: 0=all, else HTTP code
}

func parsePageQuery(q url.Values) pageQuery {
	out := pageQuery{
		Page:     1,
		PageSize: defaultPageSize,
		Filter:   "all",
	}
	if q == nil {
		return out
	}
	if v := strings.TrimSpace(q.Get("page")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			out.Page = n
		}
	}
	if v := strings.TrimSpace(firstNonEmpty(q.Get("page_size"), q.Get("limit"))); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			out.PageSize = n
		}
	}
	if out.PageSize > maxPageSize {
		out.PageSize = maxPageSize
	}
	if v := strings.TrimSpace(q.Get("filter")); v != "" {
		out.Filter = strings.ToLower(v)
	}
	out.Q = strings.TrimSpace(firstNonEmpty(q.Get("q"), q.Get("search")))
	if v := strings.TrimSpace(firstNonEmpty(q.Get("status"), q.Get("code"))); v != "" && v != "all" {
		if n, err := strconv.Atoi(v); err == nil {
			out.Status = n
		}
	}
	return out
}

func slicePage[T any](all []T, page, pageSize int) (items []T, total, pages, pageOut int) {
	total = len(all)
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	pages = total / pageSize
	if total%pageSize != 0 {
		pages++
	}
	if pages < 1 {
		pages = 1
	}
	pageOut = page
	if pageOut < 1 {
		pageOut = 1
	}
	if pageOut > pages {
		pageOut = pages
	}
	start := (pageOut - 1) * pageSize
	if start >= total {
		return []T{}, total, pages, pageOut
	}
	end := start + pageSize
	if end > total {
		end = total
	}
	return all[start:end], total, pages, pageOut
}
