package format

import (
	"gopkg.in/russross/blackfriday.v2"
	"maunium.net/go/gomatrix"
	"strings"
)

func RenderMarkdown(text string) gomatrix.Content {
	parser := blackfriday.New(
		blackfriday.WithExtensions(blackfriday.NoIntraEmphasis |
			blackfriday.Tables |
			blackfriday.FencedCode |
			blackfriday.Strikethrough |
			blackfriday.SpaceHeadings |
			blackfriday.DefinitionLists))
	ast := parser.Parse([]byte(text))

	renderer := blackfriday.NewHTMLRenderer(blackfriday.HTMLRendererParameters{
		Flags: blackfriday.UseXHTML,
	})

	var buf strings.Builder
	renderer.RenderHeader(&buf, ast)
	ast.Walk(func(node *blackfriday.Node, entering bool) blackfriday.WalkStatus {
		return renderer.RenderNode(&buf, node, entering)
	})
	renderer.RenderFooter(&buf, ast)
	htmlBody := buf.String()

	return gomatrix.Content{
		FormattedBody: htmlBody,
		Format:        gomatrix.FormatHTML,
		MsgType:       gomatrix.MsgText,
		Body:          HTMLToText(htmlBody),
	}
}
