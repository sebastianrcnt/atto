package ui

import "strconv"

// ImageResource names an existing transcript image for the authenticated
// item/image method, never a file or URL. The item ID is supplied by the engine
// in item-site props. Clients parse the zero-based suffix and request the image
// on the same thread; the server verifies transcript membership.
func ImageResource(itemID string, index int) string { return itemID + "-image-" + strconv.Itoa(index) }
