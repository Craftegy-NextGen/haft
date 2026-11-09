// Package strictclock is the wall-clock primitive layer of the haft Windows
// port (ADR-091). Some haft code takes a clock as a function, reads it once,
// does real work, and then requires the next reading to be strictly later than
// the previous one: profile declaration preparation polls its clock 64 times
// in a row for each Work and basis bound, and the manual authority act reads
// its clock once for each SpeechAct boundary. On Linux and macOS the wall clock
// has nanosecond resolution, so a reading that follows real work is strictly
// later. The Windows wall clock moves only at the system timer tick (about
// 0.8 ms as measured on Windows 10, up to 15.6 ms on other configurations), so two
// readings taken microseconds apart are equal and those requirements fail for
// real users, not only in tests (zse-6n75w).
//
// [Strict] is the one entry point. It wraps a clock so that every reading
// is strictly later than the previous reading of the same wrapped clock:
//
//   - On Linux and macOS it returns the clock it was given. Unix behavior is
//     identical to code that uses the clock directly; declcheck proves the call
//     sites are pure identity wrappers (ADR-091).
//   - On Windows, when the wrapped clock has not advanced since the previous
//     reading, it waits (a short sleep between reads) until the wrapped clock
//     moves, for at most 100 ms, and returns that reading. Every returned time
//     is a real reading of the wrapped clock; none is fabricated. If the clock
//     does not move within the limit (a frozen test clock, or a wall clock that
//     was stepped backwards) the last reading is returned unchanged, so the
//     caller reports the unadvanced clock exactly as on Unix.
//
// A nil clock stays nil on every OS. The package has no cgo and no dependency
// outside the standard library.
package strictclock
