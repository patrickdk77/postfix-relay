#!/usr/bin/perl

# Copyright 2005 Messiah College. All rights reserved.
# Jason Long <jlong@messiah.edu>

# Portions copyright (C) 2001 Morgan Stanley Dean Witter.
# Portions written by Bennett Todd <bet@rahul.net>

use strict;
use warnings;

package MySmtpServer;
use base "MSDW::SMTP::Server";

sub new
{
    my $class = shift;
    my %args = @_;
    my $self = bless \%args, $class;
	$self->{"in"} = new IO::Handle;
	$self->{"in"}->fdopen(fileno(STDIN), "r");
	$self->{"out"} = new IO::Handle;
	$self->{"out"}->fdopen(fileno(STDOUT), "w");
	$self->{"out"}->autoflush;
    $self->{"state"} = " accepted";
    return $self;
}

sub getline
{
	my ($self) = @_;
	local $/ = "\015\012";
	$/ = "\n" if ($self->{Translate});

	my $tmp = $self->{"in"}->getline;
	if (not defined $tmp)
	{
		return $tmp;
	}
	if ($self->{debug})
	{
		$self->{debug}->print($tmp);
	}
	$tmp =~ s/\n$/\015\012/ if ($self->{Translate});
	return $tmp;
}

sub print
{
	my ($self, @msg) = @_;
	my @transformed = $self->{Translate} ?
		( map { s/\015\012$/\n/; $_ } @msg ) : (@msg);
	$self->{debug}->print(@transformed) if defined $self->{debug};
	return $self->{"out"}->print(@transformed);
}

1;
