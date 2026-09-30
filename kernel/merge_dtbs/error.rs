// SPDX-FileCopyrightText: The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

//! The failures that end merge_dtbs with exit status 1.

use std::fmt;
use std::io;
use std::path::PathBuf;
use std::str::Utf8Error;

use crate::ids::{IdKind, UnsupportedProperty, PMIC_SIZE_PROPERTY};
use crate::programs::{Exit, Invocation, Program};
use crate::text::shown;

/// A failure, shown as one line after `merge_dtbs: `.
#[derive(Debug)]
pub enum MergeError {
    /// A directory option, given by its long name, names no directory.
    NotADirectory { option: &'static str, path: PathBuf },
    /// A directory of the walk cannot be listed.
    UnreadableDirectory { path: PathBuf, error: io::Error },
    /// The type of a file of the walk cannot be read.
    UnreadableFileType { path: PathBuf, error: io::Error },
    /// A program cannot be started.
    NotStarted { program: Program, error: io::Error },
    /// A program's output is no UTF-8.
    Undecodable { invocation: Invocation, error: Utf8Error },
    /// A program ended with an error; its output is passed on before the failure line.
    Failed { invocation: Invocation, exit: Exit, output: String },
    /// fdtget cannot read a file's root node.
    UnreadableRoot { path: PathBuf, message: String },
    /// fdtget shows a value of an id property that is no integer.
    NotAnInteger { path: PathBuf, property: &'static str, words: String },
    /// The root node has an id property that merge_dtbs does not handle.
    UnsupportedProperty { path: PathBuf, property: UnsupportedProperty },
    /// The pmic group size is not one positive integer.
    InvalidPmicSize { path: PathBuf, values: Vec<i64> },
    /// An id property has no values.
    EmptyProperty { path: PathBuf, kind: IdKind },
    /// The number of an id property's values is no multiple of its group size.
    IncompleteGroup { path: PathBuf, kind: IdKind, count: usize, size: usize },
    /// The name a merge is written under exists already.
    Exists { path: PathBuf },
    /// A base cannot be copied.
    CopyFailed { base: PathBuf, written: PathBuf, error: io::Error },
}

impl MergeError {
    /// The output of a failed program, which goes to standard error before the failure line.
    pub fn program_output(&self) -> Option<&str> {
        match self {
            Self::Failed { output, .. } => Some(output),
            _ => None,
        }
    }
}

impl fmt::Display for MergeError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::NotADirectory { option, path } => {
                write!(f, "--{option} {} is not a directory", shown(path))
            }
            Self::UnreadableDirectory { path, error } => {
                write!(f, "{}: cannot read the directory: {error}", shown(path))
            }
            Self::UnreadableFileType { path, error } => {
                write!(f, "{}: {error}", shown(path))
            }
            Self::NotStarted { program, error } => {
                write!(f, "{}: cannot run: {error}", program.name())
            }
            Self::Undecodable { invocation, error } => {
                write!(f, "the output of {invocation} is no UTF-8: {error}")
            }
            Self::Failed { invocation, exit, .. } => {
                write!(f, "{invocation} exited with status {exit}")
            }
            Self::UnreadableRoot { path, message } => {
                write!(f, "{}: cannot read the root node: {message}", shown(path))
            }
            Self::NotAnInteger { path, property, words } => write!(
                f,
                "{}: fdtget printed a value of {property} that is no integer: {words}",
                shown(path)
            ),
            Self::UnsupportedProperty { path, property } => write!(
                f,
                "{}: the root node names {}, an id that merge_dtbs does not handle",
                shown(path),
                property.property()
            ),
            Self::InvalidPmicSize { path, values } => {
                let values: Vec<String> = values.iter().map(i64::to_string).collect();
                write!(
                    f,
                    "{}: {PMIC_SIZE_PROPERTY} is not one positive integer: [{}]",
                    shown(path),
                    values.join(", ")
                )
            }
            Self::EmptyProperty { path, kind } => {
                write!(f, "{}: {} is empty", shown(path), kind.property())
            }
            Self::IncompleteGroup { path, kind, count, size } => write!(
                f,
                "{}: {} has {count} values, which is no multiple of {size}",
                shown(path),
                kind.property()
            ),
            Self::Exists { path } => write!(f, "{} exists already", shown(path)),
            Self::CopyFailed { base, written, error } => {
                write!(f, "cannot copy {} to {}: {error}", shown(base), shown(written))
            }
        }
    }
}
