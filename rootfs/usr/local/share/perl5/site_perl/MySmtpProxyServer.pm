#!/usr/bin/perl -I../lib
#
# Copyright (c) 2005 Messiah College. This program is free software.
# You can redistribute it and/or modify it under the terms of the
# GNU Public License as found at http://www.fsf.org/copyleft/gpl.html.
#
# Written by Jason Long, jlong@messiah.edu.

#
#   This code is Copyright (C) 2001 Morgan Stanley Dean Witter, and
#   is distributed according to the terms of the GNU Public License
#   as found at <URL:http://www.fsf.org/copyleft/gpl.html>.
#
#
#   This program is distributed in the hope that it will be useful,
#   but WITHOUT ANY WARRANTY; without even the implied warranty of
#   MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
#   GNU General Public License for more details.
#
# Written by Bennett Todd <bet@rahul.net>

use warnings;
use strict;

use MSDW::SMTP::Server;
use MSDW::SMTP::Client;
use Net::Server;
use MySmtpServer;
use IO::File;

package MySmtpProxyServer;
use base "Net::Server::MultiType";

sub run
{
	my $class = shift;
	$class->SUPER::run(@_);
}

sub process_request
{
	my $self = shift;

	my $server = $self->{smtp_server} = $self->setup_server_socket;
	my $client = $self->{smtp_client} = $self->setup_client_socket;

	# wait for SMTP greeting from destination
	my $banner = $client->hear;

	# emit greeting back to source
	$server->ok($banner);

	# begin main SMTP loop
	#  - wait for a command from source
	while (my $what = $server->chat)
	{
		if ($self->{debug})
		{
			print STDERR $what . "\n";
		}
		$self->handle_command($what)
			or last;
	}
}

sub handle_command
{
	my $self = shift;
	my ($what) = @_;
	my $server = $self->{smtp_server};
	my $client = $self->{smtp_client};

	if ($what eq '.')
	{
		if ($self->handle_end_of_data)
		{
			$server->ok($client->hear);
			return 1;
		}
		else
		{
			return undef;
		}
	}
	else
	{
	    $client->say($what);
		$server->ok($client->hear);
		return 1;
    }
}

sub setup_server_socket
{
	my $self = shift;

	# create an object for handling the incoming SMTP commands
	return new MySmtpServer;
}

# handle_end_of_data
#
# Called when the source finishes transmitting the message. This method
# may filter the message and if desired, transmit the message to
# $client. Alternatively, this method can respond to the server with
# some sort of rejection (temporary or permanent).
#
# Usage: $result = handle_end_of_data($server, $client);
#
# Returns:
#   nonzero if a message was transmitted to the next server and its response
#     returned to the source server
#   zero if the message was rejected and the connection to the next server
#     should be dropped
#
sub handle_end_of_data
{
	my $self = shift;
	my $server = $self->{smtp_server};
	my $client = $self->{smtp_client};
	my $fh = $server->{data};

	# send the message unaltered
	$fh->seek(0,0);
	$client->yammer($fh);

	return 1;
}

1;
