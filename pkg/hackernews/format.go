package hackernews

import (
	"fmt"
	"strings"
	"time"
)

const maxToolOutputBytes = 96 * 1024

func formatFeed(page FeedPage) string {
	var out strings.Builder
	fmt.Fprintf(&out, "view: %s\nstories: %d\n", page.View, len(page.Stories))
	if page.Warning != "" {
		fmt.Fprintf(&out, "warning: %s\n", cleanOneLine(page.Warning))
	}
	out.WriteByte('\n')
	formatStories(&out, page.Stories)
	return truncateBytes(strings.TrimSpace(out.String()), maxToolOutputBytes)
}

func formatSearch(page SearchPage) string {
	var out strings.Builder
	fmt.Fprintf(&out, "view: search\nquery: %s\nsort: %s\nstories: %d\n\n", cleanOneLine(page.Query), page.Sort, len(page.Stories))
	formatStories(&out, page.Stories)
	return truncateBytes(strings.TrimSpace(out.String()), maxToolOutputBytes)
}

func formatStories(out *strings.Builder, stories []Story) {
	for index, story := range stories {
		title := story.Title
		if title == "" {
			title = fmt.Sprintf("Hacker News item %d", story.ID)
		}
		fmt.Fprintf(out, "%d. %s\n", index+1, title)
		fmt.Fprintf(out, "hn_url: %s\n", ItemURL(story.ID))
		if story.URL != "" {
			fmt.Fprintf(out, "article_url: %s\n", story.URL)
		}
		fmt.Fprintf(out, "score: %d\ncomments: %d\n", story.Score, story.Comments)
		if story.By != "" {
			fmt.Fprintf(out, "by: %s\n", story.By)
		}
		if !story.PublishedAt.IsZero() {
			fmt.Fprintf(out, "published_at: %s\n", story.PublishedAt.Format(time.RFC3339))
		}
		if story.Type != "" && story.Type != "story" {
			fmt.Fprintf(out, "type: %s\n", story.Type)
		}
		if story.Text != "" {
			fmt.Fprintf(out, "text: %s\n", cleanOneLine(truncateRunes(story.Text, 500)))
		}
		out.WriteByte('\n')
	}
}

func formatThread(thread Thread) string {
	story := thread.Story
	title := story.Title
	if title == "" {
		title = fmt.Sprintf("Hacker News item %d", story.ID)
	}
	var out strings.Builder
	out.WriteString("# Hacker News item\n\n")
	writeField(&out, "Title", title)
	writeField(&out, "HN URL", ItemURL(story.ID))
	writeField(&out, "Article URL", story.URL)
	writeField(&out, "Posted by", story.By)
	if !story.PublishedAt.IsZero() {
		writeField(&out, "Published at", story.PublishedAt.Format(time.RFC3339))
	}
	fmt.Fprintf(&out, "Score: %d\nTotal comments: %d\n", story.Score, story.Comments)
	if story.Type != "" && story.Type != "story" {
		writeField(&out, "Type", story.Type)
	}
	if story.Text != "" {
		out.WriteString("\n# Hacker News post text\n\n")
		out.WriteString(story.Text)
		out.WriteByte('\n')
	}
	out.WriteString("\n# Hacker News discussion\n\n")
	fmt.Fprintf(&out, "Selection: ranked order, up to %d roots, depth %d, maximum %d comments. Hacker News does not expose comment scores.\n\n", maxTopLevelComments, maxCommentDepth, maxComments)
	if thread.Warning != "" {
		fmt.Fprintf(&out, "Warning: %s\n\n", cleanOneLine(thread.Warning))
	}
	if len(thread.Roots) == 0 {
		out.WriteString("No comments loaded.\n")
	} else {
		for _, comment := range thread.Roots {
			renderComment(&out, comment, 0)
		}
	}
	return truncateBytes(strings.TrimSpace(out.String()), maxToolOutputBytes)
}

func writeField(out *strings.Builder, name, value string) {
	value = cleanOneLine(value)
	if value != "" {
		fmt.Fprintf(out, "%s: %s\n", name, value)
	}
}

func renderComment(out *strings.Builder, comment *Comment, depth int) {
	if comment == nil {
		return
	}
	author := comment.By
	if author == "" {
		author = "unknown"
	}
	fmt.Fprintf(out, "%s- %s [item %d]: %s\n", strings.Repeat("  ", depth), author, comment.ID, cleanOneLine(comment.Text))
	for _, child := range comment.Children {
		renderComment(out, child, depth+1)
	}
	if depth == 0 {
		out.WriteByte('\n')
	}
}
