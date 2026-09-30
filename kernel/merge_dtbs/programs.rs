// SPDX-FileCopyrightText: The halogenOS Project
// SPDX-License-Identifier: Apache-2.0

//! The device tree programs merge_dtbs runs, and how it runs them.

use std::ffi::{OsStr, OsString};
use std::fmt;
use std::io::{self, Read};
use std::os::unix::process::ExitStatusExt;
use std::process::Command;

use crate::error::MergeError;
use crate::text::{self, shown};

/// A device tree program, run by name from `PATH`.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum Program {
    Fdtget,
    Fdtput,
    Fdtoverlay,
    Fdtoverlaymerge,
    UfdtApplyOverlay,
}

impl Program {
    /// The program's name.
    pub fn name(self) -> &'static str {
        match self {
            Self::Fdtget => "fdtget",
            Self::Fdtput => "fdtput",
            Self::Fdtoverlay => "fdtoverlay",
            Self::Fdtoverlaymerge => "fdtoverlaymerge",
            Self::UfdtApplyOverlay => "ufdt_apply_overlay",
        }
    }
}

/// A program with its arguments.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Invocation {
    pub program: Program,
    pub args: Vec<OsString>,
}

impl Invocation {
    /// The program without arguments.
    pub fn new(program: Program) -> Self {
        Self { program, args: Vec::new() }
    }

    /// The invocation with one more argument.
    pub fn arg(mut self, arg: impl AsRef<OsStr>) -> Self {
        self.args.push(arg.as_ref().to_owned());
        self
    }

    /// The invocation with more arguments.
    pub fn args(self, args: impl IntoIterator<Item = impl AsRef<OsStr>>) -> Self {
        args.into_iter().fold(self, Self::arg)
    }
}

/// The command line, its words separated by spaces.
impl fmt::Display for Invocation {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.program.name())?;
        self.args.iter().try_for_each(|arg| write!(f, " {}", shown(arg)))
    }
}

/// How a program ended.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Exit {
    Code(i32),
    Signal(i32),
}

impl Exit {
    /// Whether the program succeeded.
    pub fn is_success(self) -> bool {
        self == Self::Code(0)
    }
}

/// The status CPython reports: the exit code, or the negated number of the signal.
impl fmt::Display for Exit {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Code(code) => write!(f, "{code}"),
            Self::Signal(signal) => write!(f, "-{signal}"),
        }
    }
}

/// A program that ran: how it ended, and its standard output and standard error together.
pub struct Finished {
    pub exit: Exit,
    pub output: Vec<u8>,
}

/// Runs the device tree programs.
pub trait Programs {
    /// Runs a program with standard input inherited and standard output and standard error
    /// captured together, and waits for it. An error means the program did not run.
    fn run(&mut self, invocation: &Invocation) -> io::Result<Finished>;
}

/// The programs of the operating system, found through a search path.
pub struct SystemPrograms {
    /// The directories a program is looked up in, `PATH`'s form; None for the operating system's
    /// default. The programs get it as their `PATH`.
    search_path: Option<OsString>,
}

impl SystemPrograms {
    /// The programs found through this search path.
    pub fn new(search_path: Option<OsString>) -> Self {
        Self { search_path }
    }
}

impl Programs for SystemPrograms {
    fn run(&mut self, invocation: &Invocation) -> io::Result<Finished> {
        let (mut reader, writer) = io::pipe()?;
        let mut command = Command::new(invocation.program.name());
        if let Some(search_path) = &self.search_path {
            command.env("PATH", search_path);
        }
        command.args(&invocation.args).stdout(writer.try_clone()?).stderr(writer);
        let mut child = command.spawn()?;
        // The command has this process's write ends of the pipe; without them, the read ends when
        // the program has closed its own.
        drop(command);
        let mut output = Vec::new();
        let read = reader.read_to_end(&mut output);
        drop(reader);
        let status = child.wait()?;
        read?;
        let exit = match (status.code(), status.signal()) {
            (Some(code), _) => Exit::Code(code),
            (None, Some(signal)) => Exit::Signal(signal),
            (None, None) => {
                return Err(io::Error::other(format!("invalid wait status: {}", status.into_raw())))
            }
        };
        Ok(Finished { exit, output })
    }
}

/// A program's end and its output as text.
pub struct Captured {
    pub exit: Exit,
    pub output: String,
}

/// Runs a program, see `Programs::run`, and reads its output as text.
pub fn run_captured(
    programs: &mut dyn Programs,
    invocation: &Invocation,
) -> Result<Captured, MergeError> {
    let finished = programs
        .run(invocation)
        .map_err(|error| MergeError::NotStarted { program: invocation.program, error })?;
    let output = text::decode_output(finished.output)
        .map_err(|error| MergeError::Undecodable { invocation: invocation.clone(), error })?;
    Ok(Captured { exit: finished.exit, output })
}

/// Runs a program that has to succeed; its output.
pub fn run_program(
    programs: &mut dyn Programs,
    invocation: Invocation,
) -> Result<String, MergeError> {
    let captured = run_captured(programs, &invocation)?;
    if captured.exit.is_success() {
        Ok(captured.output)
    } else {
        Err(MergeError::Failed { invocation, exit: captured.exit, output: captured.output })
    }
}

#[cfg(test)]
mod tests {
    use std::fs;
    use std::os::unix::fs::PermissionsExt;

    use super::*;
    use crate::recorder::Recorder;
    use crate::scratch_dir::ScratchDir;

    #[test]
    fn invocation_and_exit_text() {
        let invocation = Invocation::new(Program::UfdtApplyOverlay).args(["a b.dtb", "c.dtbo"]);
        assert_eq!(invocation.to_string(), "ufdt_apply_overlay a b.dtb c.dtbo");
        assert_eq!(Exit::Code(2).to_string(), "2");
        assert_eq!(Exit::Signal(11).to_string(), "-11");
        assert!(Exit::Code(0).is_success() && !Exit::Signal(0).is_success());
    }

    #[test]
    fn program_failures() {
        let invocation = || Invocation::new(Program::Fdtput).arg("x.dtb");
        let mut programs = Recorder::default().failing(Program::Fdtput, Exit::Signal(9), b"out\n");
        let error = run_program(&mut programs, invocation()).unwrap_err();
        assert_eq!(error.program_output(), Some("out\n"));
        assert_eq!(error.to_string(), "fdtput x.dtb exited with status -9");
        let mut programs = Recorder::default().not_starting(2);
        let error = run_program(&mut programs, invocation()).unwrap_err();
        assert_eq!(error.program_output(), None);
        assert_eq!(error.to_string(), "fdtput: cannot run: No such file or directory (os error 2)");
        let mut programs = Recorder::default().failing(Program::Fdtput, Exit::Code(0), b"ok\xff");
        let error = run_program(&mut programs, invocation()).unwrap_err();
        assert_eq!(
            error.to_string(),
            "the output of fdtput x.dtb is no UTF-8: invalid utf-8 sequence of 1 bytes from index 2"
        );
    }

    /// Runs the programs of a scratch directory, each a shell script, with nothing else on the
    /// search path. All scripts are written before the first one runs, so that no program starts
    /// while a script is open for writing.
    #[test]
    fn system_programs() {
        let scratch = ScratchDir::new("system_programs");
        let scripts = [
            // Four MiB of output, more than a pipe buffers, from shell builtins only, and a
            // failure.
            (
                Program::Fdtget,
                "s=x\nfor i in 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22; do\n\
                 s=$s$s\ndone\nprintf %s \"$s\"\nexit 3\n",
            ),
            // A program that a signal ends.
            (Program::Fdtput, "printf partial\nkill -KILL $$\n"),
            // Standard output and standard error into one buffer, in the order of the writes.
            (Program::Fdtoverlay, "printf a\nprintf b >&2\nprintf c\nprintf d >&2\n"),
            // The arguments, one to a line.
            (Program::Fdtoverlaymerge, "for argument in \"$@\"; do echo \"$argument\"; done\n"),
        ];
        for (program, body) in scripts {
            let path = scratch.path().join(program.name());
            fs::write(&path, format!("#!/bin/sh\n{body}")).unwrap();
            fs::set_permissions(&path, fs::Permissions::from_mode(0o755)).unwrap();
        }
        let mut programs = SystemPrograms::new(Some(scratch.path().into()));
        let mut run = |program| programs.run(&Invocation::new(program)).unwrap();

        let large = run(Program::Fdtget);
        assert_eq!(large.exit, Exit::Code(3));
        assert_eq!(large.output.len(), 1 << 22);
        assert!(large.output.iter().all(|&byte| byte == b'x'));

        let killed = run(Program::Fdtput);
        assert_eq!((killed.exit, killed.output.as_slice()), (Exit::Signal(9), &b"partial"[..]));

        let interleaved = run(Program::Fdtoverlay);
        assert_eq!(
            (interleaved.exit, interleaved.output.as_slice()),
            (Exit::Code(0), &b"abcd"[..])
        );

        let invocation = Invocation::new(Program::Fdtoverlaymerge).args(["a b", "c"]);
        let arguments = programs.run(&invocation).unwrap();
        assert_eq!(
            (arguments.exit, arguments.output.as_slice()),
            (Exit::Code(0), &b"a b\nc\n"[..])
        );

        // A program the search path lacks does not start.
        let error = programs.run(&Invocation::new(Program::UfdtApplyOverlay)).err().unwrap();
        assert_eq!(error.kind(), io::ErrorKind::NotFound);
    }
}
