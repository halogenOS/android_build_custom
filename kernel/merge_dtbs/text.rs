// SPDX-FileCopyrightText: The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

//! Text as merge_dtbs reads and writes it: the output of the device tree programs is strict UTF-8
//! with universal newlines, and its words and lines are split at CPython's white space and line
//! breaks. A file name shows each byte that is no UTF-8 as the surrogate escape `\udcXX`, and file
//! names sort as CPython sorts them.

use std::ffi::OsStr;
use std::fmt;
use std::os::unix::ffi::OsStrExt;
use std::str::Utf8Error;

/// Whether CPython counts a character as white space: Unicode's white space and the information
/// separators U+001C to U+001F.
fn is_whitespace(c: char) -> bool {
    c.is_whitespace() || ('\u{1c}'..='\u{1f}').contains(&c)
}

/// Whether `str.splitlines` ends a line at a character.
fn is_line_break(c: char) -> bool {
    matches!(
        c,
        '\n' | '\u{b}'
            | '\u{c}'
            | '\r'
            | '\u{1c}'
            | '\u{1d}'
            | '\u{1e}'
            | '\u{85}'
            | '\u{2028}'
            | '\u{2029}'
    )
}

/// The words of a text, `str.split()`.
pub fn words(text: &str) -> impl Iterator<Item = &str> {
    text.split(is_whitespace).filter(|word| !word.is_empty())
}

/// The lines of a text that have more than white space, each without the white space around it,
/// joined with single spaces; None when there are none.
pub fn joined_lines(text: &str) -> Option<String> {
    let lines: Vec<&str> = text
        .split(is_line_break)
        .map(|line| line.trim_matches(is_whitespace))
        .filter(|line| !line.is_empty())
        .collect();
    (!lines.is_empty()).then(|| lines.join(" "))
}

/// The integer of a word of an optional sign and ASCII digits, the form fdtget shows; None for
/// any other word and for a value outside 64 bits.
pub fn integer(word: &str) -> Option<i64> {
    word.parse().ok()
}

/// Program output as text: strict UTF-8, with `\r\n` and `\r` read as `\n`.
pub fn decode_output(output: Vec<u8>) -> Result<String, Utf8Error> {
    let text = String::from_utf8(output).map_err(|error| error.utf8_error())?;
    if text.contains('\r') {
        Ok(text.replace("\r\n", "\n").replace('\r', "\n"))
    } else {
        Ok(text)
    }
}

/// An operating system string as CPython writes it to standard error: its UTF-8 as it is, and
/// each other byte as the escape of the surrogate it reads the byte as.
pub struct Shown<'a>(pub &'a OsStr);

impl fmt::Display for Shown<'_> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        for chunk in self.0.as_bytes().utf8_chunks() {
            f.write_str(chunk.valid())?;
            for byte in chunk.invalid() {
                write!(f, "\\udc{byte:02x}")?;
            }
        }
        Ok(())
    }
}

/// Shows an operating system string, see `Shown`.
pub fn shown(text: &(impl AsRef<OsStr> + ?Sized)) -> Shown<'_> {
    Shown(text.as_ref())
}

/// An operating system string as text, see `Shown`.
#[cfg(test)]
pub fn shown_text(text: &(impl AsRef<OsStr> + ?Sized)) -> String {
    shown(text).to_string()
}

/// The key CPython sorts a file name by: its code points, each byte that is no UTF-8 read as the
/// surrogate U+DC00 plus the byte.
pub fn sort_key(name: &OsStr) -> Vec<u32> {
    name.as_bytes()
        .utf8_chunks()
        .flat_map(|chunk| {
            let valid = chunk.valid().chars().map(u32::from);
            valid.chain(chunk.invalid().iter().map(|&byte| 0xdc00 + u32::from(byte)))
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use std::os::unix::ffi::OsStrExt;

    use super::*;

    // The expected results are those of CPython 3.13 for the same input.

    #[test]
    fn split() {
        let text = "a\u{1c}b\u{85}c\u{3000}d\u{200b}e  f\u{1f}g";
        assert_eq!(words(text).collect::<Vec<_>>(), ["a", "b", "c", "d\u{200b}e", "f", "g"]);
        let text = " one \u{b}two\u{c}\u{1d} three \u{2028}\n\n four \u{85}";
        assert_eq!(joined_lines(text).as_deref(), Some("one two three four"));
        assert_eq!(joined_lines(" \n\t\n"), None);
    }

    #[test]
    fn integers() {
        for (word, expected) in [
            ("12", Some(12)),
            ("-7", Some(-7)),
            ("+3", Some(3)),
            ("007", Some(7)),
            ("9223372036854775807", Some(i64::MAX)),
            ("-9223372036854775808", Some(i64::MIN)),
            ("9223372036854775808", None),
            ("1_000", None),
            ("0x1", None),
            ("", None),
            ("-", None),
            ("1.0", None),
        ] {
            assert_eq!(integer(word), expected, "{word:?}");
        }
    }

    #[test]
    fn decode() {
        assert_eq!(decode_output(b"a\r\nb\rc\n\rd".to_vec()).unwrap(), "a\nb\nc\n\nd");
        // The error has the position of the first byte that is no UTF-8.
        for (output, position, length) in
            [(&b"\xff"[..], 0, Some(1)), (b"ab\xe2\x82", 2, None), (b"a\xed\xa0\x80", 1, Some(1))]
        {
            let error = decode_output(output.to_vec()).unwrap_err();
            assert_eq!((error.valid_up_to(), error.error_len()), (position, length), "{output:?}");
        }
    }

    #[test]
    fn shown_bytes() {
        let raw = OsStr::from_bytes(b"a\xffb\xe2\x82");
        assert_eq!(shown(raw).to_string(), "a\\udcffb\\udce2\\udc82");
    }

    #[test]
    fn name_order() {
        let mut names: Vec<&OsStr> = [&b"z"[..], b"\xef\x80\x80", b"\xff", b"\xc3\xa9", b"B", b"a"]
            .into_iter()
            .map(OsStr::from_bytes)
            .collect();
        names.sort_by_key(|name| sort_key(name));
        // U+DCFF for the byte 0xff sorts before U+F000, though the byte sorts after its UTF-8.
        let expected: [&[u8]; 6] = [b"B", b"a", b"z", b"\xc3\xa9", b"\xff", b"\xef\x80\x80"];
        assert_eq!(names, expected.map(OsStr::from_bytes));
    }
}
