package vfs

import (
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/cozy/cozy-stack/couchdb"
	"github.com/cozy/cozy-stack/couchdb/mango"
	"github.com/cozy/cozy-stack/web/jsonapi"
	"github.com/spf13/afero"
)

// DirDoc is a struct containing all the informations about a
// directory. It implements the couchdb.Doc and jsonapi.Object
// interfaces.
type DirDoc struct {
	// Type of document. Useful to (de)serialize and filter the data
	// from couch.
	Type string `json:"type"`
	// Qualified file identifier
	ObjID string `json:"_id,omitempty"`
	// Directory revision
	ObjRev string `json:"_rev,omitempty"`
	// Directory name
	Name string `json:"name"`
	// Parent folder identifier
	FolderID string `json:"folder_id"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Directory path on VFS
	Path string   `json:"path"`
	Tags []string `json:"tags"`

	parent *DirDoc
	files  []*FileDoc
	dirs   []*DirDoc
}

// ID returns the directory qualified identifier (part of couchdb.Doc interface)
func (d *DirDoc) ID() string {
	return d.ObjID
}

// Rev returns the directory revision (part of couchdb.Doc interface)
func (d *DirDoc) Rev() string {
	return d.ObjRev
}

// DocType returns the directory document type (part of couchdb.Doc
// interface)
func (d *DirDoc) DocType() string {
	return FsDocType
}

// SetID is used to change the directory qualified identifier (part of
// couchdb.Doc interface)
func (d *DirDoc) SetID(id string) {
	d.ObjID = id
}

// SetRev is used to change the directory revision (part of
// couchdb.Doc interface)
func (d *DirDoc) SetRev(rev string) {
	d.ObjRev = rev
}

// SelfLink is used to generate a JSON-API link for the directory (part of
// jsonapi.Object interface)
func (d *DirDoc) SelfLink() string {
	return "/files/" + d.ObjID
}

// Relationships is used to generate the content relationship in JSON-API format
// (part of the jsonapi.Object interface)
//
// TODO: pagination
func (d *DirDoc) Relationships() jsonapi.RelationshipMap {
	l := len(d.files) + len(d.dirs)
	i := 0

	data := make([]jsonapi.ResourceIdentifier, l)
	for _, child := range d.dirs {
		data[i] = jsonapi.ResourceIdentifier{ID: child.ID(), Type: child.DocType()}
		i++
	}

	for _, child := range d.files {
		data[i] = jsonapi.ResourceIdentifier{ID: child.ID(), Type: child.DocType()}
		i++
	}

	contents := jsonapi.Relationship{Data: data}

	var parent jsonapi.Relationship
	if d.ID() != RootFolderID {
		parent = jsonapi.Relationship{
			Links: &jsonapi.LinksList{
				Related: "/files/" + d.FolderID,
			},
			Data: jsonapi.ResourceIdentifier{
				ID:   d.FolderID,
				Type: FsDocType,
			},
		}
	}

	return jsonapi.RelationshipMap{
		"parent":   parent,
		"contents": contents,
	}
}

// Included is part of the jsonapi.Object interface
func (d *DirDoc) Included() []jsonapi.Object {
	var included []jsonapi.Object
	for _, child := range d.dirs {
		included = append(included, child)
	}
	for _, child := range d.files {
		included = append(included, child)
	}
	return included
}

// FetchFiles is used to fetch direct children of the directory.
//
// @TODO: add pagination control
func (d *DirDoc) FetchFiles(c *Context) (err error) {
	d.files, d.dirs, err = fetchChildren(c, d)
	return err
}

// NewDirDoc is the DirDoc constructor. The given name is validated.
func NewDirDoc(name, folderID string, tags []string, parent *DirDoc) (doc *DirDoc, err error) {
	if err = checkFileName(name); err != nil {
		return
	}

	if folderID == "" {
		folderID = RootFolderID
	}

	if folderID == RootFolderID && parent == nil {
		parent = getRootDirDoc()
	}

	createDate := time.Now()
	doc = &DirDoc{
		Type:     DirType,
		Name:     name,
		FolderID: folderID,

		CreatedAt: createDate,
		UpdatedAt: createDate,
		Tags:      tags,

		parent: parent,
	}

	return
}

// GetDirDoc is used to fetch directory document information
// form the database.
func GetDirDoc(c *Context, fileID string, withChildren bool) (*DirDoc, error) {
	if fileID == RootFolderID {
		return getRootDirDoc(), nil
	}
	doc := &DirDoc{}
	err := couchdb.GetDoc(c.db, FsDocType, fileID, doc)
	if couchdb.IsNotFoundError(err) {
		err = ErrParentDoesNotExist
	}
	if err != nil {
		return nil, err
	}
	if doc.Type != DirType {
		return nil, os.ErrNotExist
	}
	if withChildren {
		err = doc.FetchFiles(c)
	}
	return doc, err
}

// GetDirDocFromPath is used to fetch directory document information from
// the database from its path.
func GetDirDocFromPath(c *Context, pth string, withChildren bool) (*DirDoc, error) {
	var doc *DirDoc
	var err error
	if pth == "/" {
		doc = getRootDirDoc()
	} else {
		var docs []*DirDoc
		sel := mango.Equal("path", path.Clean(pth))
		req := &couchdb.FindRequest{Selector: sel, Limit: 1}
		err = couchdb.FindDocs(c.db, FsDocType, req, &docs)
		if err != nil {
			return nil, err
		}
		if len(docs) == 0 {
			return nil, os.ErrNotExist
		}
		doc = docs[0]
	}
	if withChildren {
		err = doc.FetchFiles(c)
	}
	return doc, err
}

// CreateDirectory is the method for creating a new directory
func CreateDirectory(c *Context, doc *DirDoc) (err error) {
	pth, _, err := getFilePath(c, doc.Name, doc.FolderID)
	if err != nil {
		return err
	}

	err = c.fs.Mkdir(pth, 0755)
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			c.fs.Remove(pth)
		}
	}()

	doc.Path = pth

	return couchdb.CreateDoc(c.db, doc)
}

// ModifyDirectoryMetadata modify the metadata associated to a
// directory. It can be used to rename or move the directory in the
// VFS.
func ModifyDirectoryMetadata(c *Context, olddoc *DirDoc, data *DocMetaAttributes) (newdoc *DirDoc, err error) {
	pth := olddoc.Path
	name := olddoc.Name
	tags := olddoc.Tags
	folderID := olddoc.FolderID
	mdate := olddoc.UpdatedAt
	parent := olddoc.parent

	if data.FolderID != nil && *data.FolderID != folderID {
		folderID = *data.FolderID
		pth, parent, err = getFilePath(c, name, folderID)
		if err != nil {
			return
		}
	}

	if data.Name != "" {
		name = data.Name
		pth = path.Join(path.Dir(pth), name)
	}

	if data.Tags != nil {
		tags = appendTags(tags, data.Tags)
	}

	if data.UpdatedAt != nil {
		mdate = *data.UpdatedAt
	}

	if mdate.Before(olddoc.CreatedAt) {
		err = ErrIllegalTime
		return
	}

	newdoc, err = NewDirDoc(name, folderID, tags, parent)
	if err != nil {
		return
	}

	newdoc.SetID(olddoc.ID())
	newdoc.SetRev(olddoc.Rev())
	newdoc.CreatedAt = olddoc.CreatedAt
	newdoc.UpdatedAt = mdate
	newdoc.Path = pth
	newdoc.files = olddoc.files
	newdoc.dirs = olddoc.dirs

	if pth != olddoc.Path {
		err = safeRenameDirectory(olddoc.Path, pth, c.fs)
		if err != nil {
			return
		}
	}

	err = bulkUpdateDocsPath(c, olddoc, pth)
	if err != nil {
		return
	}

	err = couchdb.UpdateDoc(c.db, newdoc)
	return
}

// @TODO remove this method and use couchdb bulk updates instead
func bulkUpdateDocsPath(c *Context, olddoc *DirDoc, newpath string) error {
	oldpath := path.Clean(olddoc.Path)

	var children []*DirDoc
	sel := mango.StartWith("path", oldpath+"/")
	req := &couchdb.FindRequest{Selector: sel}
	err := couchdb.FindDocs(c.db, FsDocType, req, &children)
	if err != nil || len(children) == 0 {
		return err
	}

	errc := make(chan error)

	for _, child := range children {
		go func(child *DirDoc) {
			if !strings.HasPrefix(child.Path, oldpath+"/") {
				errc <- fmt.Errorf("Child has wrong base directory")
			} else {
				child.Path = path.Join(newpath, child.Path[len(oldpath)+1:])
				errc <- couchdb.UpdateDoc(c.db, child)
			}
		}(child)
	}

	for range children {
		if e := <-errc; e != nil {
			err = e
		}
	}

	return err
}

func fetchChildren(c *Context, parent *DirDoc) (files []*FileDoc, dirs []*DirDoc, err error) {
	var docs []*dirOrFile
	sel := mango.Equal("folder_id", parent.ID())
	req := &couchdb.FindRequest{Selector: sel, Limit: 10}
	err = couchdb.FindDocs(c.db, FsDocType, req, &docs)
	if err != nil {
		return
	}

	for _, doc := range docs {
		typ, dir, file := doc.refine()
		switch typ {
		case FileType:
			file.parent = parent
			files = append(files, file)
		case DirType:
			dir.parent = parent
			dirs = append(dirs, dir)
		}
	}

	return
}

func safeRenameDirectory(oldpath, newpath string, fs afero.Fs) error {
	newpath = path.Clean(newpath)
	oldpath = path.Clean(oldpath)

	if !path.IsAbs(newpath) || !path.IsAbs(oldpath) {
		return fmt.Errorf("paths should be absolute")
	}

	if strings.HasPrefix(newpath, oldpath+"/") {
		return ErrForbiddenDocMove
	}

	_, err := fs.Stat(newpath)
	if err == nil {
		return os.ErrExist
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	return fs.Rename(oldpath, newpath)
}

var (
	_ couchdb.Doc    = &DirDoc{}
	_ jsonapi.Object = &DirDoc{}
)
