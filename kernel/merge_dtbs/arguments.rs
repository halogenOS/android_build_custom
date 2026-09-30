// SPDX-FileCopyrightText: The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

//! The command line: `--base`, `--techpack` and `--out` with one directory each, all three
//! required, and `-h` or `--help`.

use std::ffi::OsString;
use std::path::{Path, PathBuf};

use clap::Parser;

/// The long name of the option that gives the base directory.
const BASE: &str = "base";
/// The long name of the option that gives the techpack directory.
const TECHPACK: &str = "techpack";
/// The long name of the option that gives the out directory.
const OUT: &str = "out";

/// Merges techpack device tree overlays into base device trees.
#[derive(Debug, Parser, PartialEq, Eq)]
#[command(name = "merge_dtbs", no_binary_name = true)]
pub struct Directories {
    /// The directory of the base device tree files
    #[arg(long = BASE)]
    pub base: PathBuf,
    /// The directory of the overlay files
    #[arg(long = TECHPACK)]
    pub techpack: PathBuf,
    /// The existing directory the merged files are written into
    #[arg(long = OUT)]
    pub out: PathBuf,
}

impl Directories {
    /// The directories, each with the long name of the option that gives it.
    pub fn with_options(&self) -> [(&'static str, &Path); 3] {
        [(BASE, &self.base), (TECHPACK, &self.techpack), (OUT, &self.out)]
    }
}

/// Reads the command line arguments after the program name. The error is the help for `-h` and
/// `--help`, and a usage error otherwise.
pub fn read(arguments: &[OsString]) -> Result<Directories, clap::Error> {
    Directories::try_parse_from(arguments)
}

#[cfg(test)]
mod tests {
    use std::os::unix::ffi::OsStringExt;

    use clap::error::ErrorKind;

    use super::*;

    fn read_words(words: &[&str]) -> Result<Directories, clap::Error> {
        read(&words.iter().map(OsString::from).collect::<Vec<_>>())
    }

    fn error_kind(words: &[&str]) -> ErrorKind {
        read_words(words).unwrap_err().kind()
    }

    #[test]
    fn options() {
        let expected = Directories { base: "x".into(), techpack: "y".into(), out: "z".into() };
        assert_eq!(
            read_words(&["--base", "x", "--techpack", "y", "--out", "z"]).unwrap(),
            expected
        );
        assert_eq!(
            read_words(&["--out", "z", "--base", "x", "--techpack", "y"]).unwrap(),
            expected
        );
        assert_eq!(read_words(&["--base=x", "--techpack=y", "--out=z"]).unwrap(), expected);
    }

    #[test]
    fn required_options() {
        let error = read_words(&["--base", "x", "--techpack", "y"]).unwrap_err();
        assert_eq!(error.kind(), ErrorKind::MissingRequiredArgument);
        assert!(error.to_string().contains("--out <OUT>"), "{error}");
        assert_eq!(error_kind(&[]), ErrorKind::MissingRequiredArgument);
    }

    #[test]
    fn rejected() {
        assert_eq!(
            error_kind(&["--base", "x", "--techpack", "y", "--out", "z", "--unknown"]),
            ErrorKind::UnknownArgument
        );
        assert_eq!(
            error_kind(&["--base", "x", "--techpack", "y", "--out", "z", "extra"]),
            ErrorKind::UnknownArgument
        );
        assert_eq!(
            error_kind(&["--techpack", "y", "--out", "z", "--base"]),
            ErrorKind::InvalidValue
        );
        // Shortened option names are no options.
        assert_eq!(
            error_kind(&["--b", "x", "--techpack", "y", "--out", "z"]),
            ErrorKind::UnknownArgument
        );
        let error = read_words(&["--base", "x"]).unwrap_err();
        assert!(error.use_stderr());
        assert_eq!(error.exit_code(), 2);
    }

    #[test]
    fn help() {
        for words in [&["-h"][..], &["--help"], &["--base", "x", "-h"]] {
            let error = read_words(words).unwrap_err();
            assert_eq!(error.kind(), ErrorKind::DisplayHelp, "{words:?}");
            assert!(!error.use_stderr());
            assert_eq!(error.exit_code(), 0);
        }
        let help = read_words(&["-h"]).unwrap_err().render().to_string();
        assert!(help.contains("Usage: merge_dtbs --base <BASE> --techpack <TECHPACK> --out <OUT>"));
        assert!(help.contains("The directory of the overlay files"), "{help}");
    }

    #[test]
    fn bytes_that_are_no_utf8() {
        // A value is passed on byte for byte.
        let raw = OsString::from_vec(b"/tmp/\xff".to_vec());
        let mut arguments: Vec<OsString> =
            ["--base", "x", "--techpack", "y", "--out", "z"].map(OsString::from).into();
        arguments[1] = raw.clone();
        arguments[5] = raw.clone();
        let directories = read(&arguments).unwrap();
        assert_eq!(directories.base.as_os_str(), raw);
        assert_eq!(directories.out.as_os_str(), raw);
    }
}
