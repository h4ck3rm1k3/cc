package parse

// Parser and validator for C source code.
//
// Misc notes:
//
// Typedef is actually a storage class, it just means instead of storing a value,
// it declares a named type.
//
// Glossary:
//
// Declarator
// ----------
//
// A declarator is the part of a declaration that specifies
// the name that is to be introduced into the program.
//
// e.g.
// unsigned int a, *b, **c, *const*d *volatile*e ;
//              ^  ^^  ^^^  ^^^^^^^^ ^^^^^^^^^^^
//
// Direct Declarator
// -----------------
//
// A direct declarator is missing the pointer prefix.
//
// e.g.
// unsigned int a[32], b[];
//              ^^^^^  ^^^
//
// Abstract Declarator
// -------------------
//
// A delcarator missing an identifier.
