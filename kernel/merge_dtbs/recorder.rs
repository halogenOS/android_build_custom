// SPDX-FileCopyrightText: The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

//! A stand-in for the device tree programs in tests. It records every invocation and keeps the
//! root node properties of the files, given for the inputs and set by fdtput, which fdtget shows.
//! A program given a failure ends with its exit and output, and with an operating system error
//! set, no program starts.

use std::collections::HashMap;
use std::ffi::OsString;
use std::io;
use std::path::{Path, PathBuf};

use crate::programs::{Exit, Finished, Invocation, Program, Programs};

#[derive(Default)]
pub struct Recorder {
    pub invocations: Vec<Invocation>,
    /// Per file, its root node properties in order, each with the words fdtget shows.
    properties: HashMap<PathBuf, Vec<(String, Vec<String>)>>,
    failures: HashMap<Program, (Exit, Vec<u8>)>,
    start_error: Option<i32>,
}

impl Recorder {
    /// A recorder with the root node properties of these files.
    pub fn with_files<'a>(
        files: impl IntoIterator<Item = (&'a Path, Vec<(&'a str, &'a str)>)>,
    ) -> Self {
        let properties = files
            .into_iter()
            .map(|(path, properties)| {
                let properties = properties
                    .into_iter()
                    .map(|(name, words)| (name.to_owned(), words_of(words)))
                    .collect();
                (path.to_owned(), properties)
            })
            .collect();
        Self { properties, ..Self::default() }
    }

    /// The recorder with a program that ends with this exit and output.
    pub fn failing(mut self, program: Program, exit: Exit, output: &[u8]) -> Self {
        self.failures.insert(program, (exit, output.to_vec()));
        self
    }

    /// The recorder with no program starting, for this operating system error number.
    pub fn not_starting(mut self, error: i32) -> Self {
        self.start_error = Some(error);
        self
    }

    /// The invocations of one program, in order, each as its words: the program's name and its
    /// arguments.
    pub fn invocations_of(&self, program: Program) -> Vec<Vec<OsString>> {
        self.invocations
            .iter()
            .filter(|invocation| invocation.program == program)
            .map(|invocation| {
                let name = OsString::from(invocation.program.name());
                [name].into_iter().chain(invocation.args.iter().cloned()).collect()
            })
            .collect()
    }

    fn property_words(&self, path: &str, name: &str) -> String {
        let properties = &self.properties[Path::new(path)];
        let (_, words) = properties.iter().find(|(property, _)| property == name).unwrap();
        words.join(" ")
    }
}

fn words_of(words: &str) -> Vec<String> {
    words.split(' ').filter(|word| !word.is_empty()).map(str::to_owned).collect()
}

impl Programs for Recorder {
    fn run(&mut self, invocation: &Invocation) -> io::Result<Finished> {
        self.invocations.push(invocation.clone());
        if let Some(error) = self.start_error {
            return Err(io::Error::from_raw_os_error(error));
        }
        if let Some((exit, output)) = self.failures.get(&invocation.program) {
            return Ok(Finished { exit: *exit, output: output.clone() });
        }
        let args: Vec<&str> = invocation.args.iter().map(|arg| arg.to_str().unwrap()).collect();
        let output = match (invocation.program, args.as_slice()) {
            (Program::Fdtget, ["-p", path, "/"]) => self
                .properties
                .get(Path::new(path))
                .map(|properties| properties.iter().map(|(name, _)| format!("{name}\n")).collect())
                .unwrap_or_default(),
            (Program::Fdtget, ["-t", "i", path, "/", name]) => {
                format!("{}\n", self.property_words(path, name))
            }
            (Program::Fdtput, ["-t", "i", path, "/", name, values @ ..]) => {
                let properties = self.properties.entry(PathBuf::from(path)).or_default();
                properties.retain(|(property, _)| property != name);
                properties.push((name.to_string(), values.iter().map(|&v| v.to_owned()).collect()));
                String::new()
            }
            _ => String::new(),
        };
        Ok(Finished { exit: Exit::Code(0), output: output.into_bytes() })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn invocation_words() {
        let mut recorder = Recorder::default();
        let invocation = Invocation::new(Program::UfdtApplyOverlay).args(["a b.dtb", "c.dtbo"]);
        recorder.run(&invocation).unwrap();
        assert_eq!(
            recorder.invocations_of(Program::UfdtApplyOverlay),
            [["ufdt_apply_overlay", "a b.dtb", "c.dtbo"].map(OsString::from)]
        );
        assert!(recorder.invocations_of(Program::Fdtget).is_empty());
    }
}
