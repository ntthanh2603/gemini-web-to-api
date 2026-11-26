package providers

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ImageEditRequest is the provider-level input for an image edit (image + text → image).
type ImageEditRequest struct {
	Prompt string
	Model  string
	Size   string
	Files  []InputFile
}

// ImageEditResult mirrors the text-to-image result for endpoint parity.
type ImageEditResult struct {
	Created int64
	Data    []Image
}

// buildImageEditPrompt turns an edit instruction plus the names of the
// reference images (in upload order) into one prompt for Gemini Web.
//
// Gemini Web has no dedicated image-edit RPC: an edit is an ordinary
// generation with the reference images attached, so the prompt is the only
// place that can tell the model what each attachment is. Numbering them in
// upload order gives the instruction something stable to refer to ("make the
// cat in Image 1 wear a hat"), which matters as soon as there is more than one.
func buildImageEditPrompt(prompt string, fileNames []string, size string) string {
	var b strings.Builder
	b.WriteString("Edit the provided image(s) according to this instruction. ")
	b.WriteString("Generate and return the edited image; do not reply with text only.\n\n")
	b.WriteString("Instruction: ")
	b.WriteString(strings.TrimSpace(prompt))
	if len(fileNames) > 0 {
		b.WriteString("\n\nReference images (attached, in order):")
		for i, name := range fileNames {
			b.WriteString(fmt.Sprintf("\nImage %d: %s", i+1, name))
		}
	}
	if s := strings.TrimSpace(size); s != "" {
		b.WriteString("\nRequested size/aspect: ")
		b.WriteString(s)
	}
	return b.String()
}

// CreateImageEdit edits reference images with a text instruction and returns
// the generated result(s) in the same shape as text-to-image generation.
//
// It reuses the ordinary generation path: the files are uploaded, attached to
// the request, and any generated image is downloaded through the signed chain
// so callers get bytes (Image.B64JSON), not just a preview URL.
func (c *Client) CreateImageEdit(ctx context.Context, req ImageEditRequest) (*ImageEditResult, error) {
	if c.imageEditFn != nil {
		return c.imageEditFn(ctx, req)
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	if len(req.Files) == 0 {
		return nil, fmt.Errorf("at least one image is required")
	}

	// Name the references in the prompt before uploading so the numbering
	// matches the order the attachments end up in.
	names := make([]string, 0, len(req.Files))
	for i, f := range req.Files {
		name := strings.TrimSpace(f.Name)
		if name == "" {
			name = fmt.Sprintf("image_%d", i+1)
		}
		names = append(names, name)
	}

	opts := []GenerateOption{
		WithInputFiles(req.Files),
		WithGeneratedImageDownload(true),
	}
	if req.Model != "" {
		opts = append(opts, WithModel(req.Model))
	}

	response, err := c.GenerateContent(ctx, buildImageEditPrompt(prompt, names, req.Size), opts...)
	if err != nil {
		return nil, err
	}

	var images []Image
	for _, image := range response.Images {
		if image.Generated {
			images = append(images, image)
		}
	}
	if len(images) == 0 {
		return nil, fmt.Errorf("provider returned no edited images")
	}
	return &ImageEditResult{Created: time.Now().Unix(), Data: images}, nil
}

// SetImageEditFunc replaces the image-edit implementation; used by tests of the
// HTTP layer to capture requests without touching the network.
func (c *Client) SetImageEditFunc(fn func(ctx context.Context, req ImageEditRequest) (*ImageEditResult, error)) {
	c.imageEditFn = fn
}
