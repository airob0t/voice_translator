package main

// TextCallback is a callback function for source and translation text updates
type TextCallback func(sourceText, translationText string)

// ErrorCallback is a callback function for error notifications
type ErrorCallback func(err error)
